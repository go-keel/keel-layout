# Asynchronous and scheduled tasks (asynq)

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before adding work that runs later, delayed or on a schedule (sending mail, building a report, cleaning
expired data, a daily reconciliation).

What kasynq does (task contracts, enqueue options, retries, cron, shutdown, the Redis eviction check) is in
[Tasks](keel:docs/tasks.md); this guide only covers where each piece goes in this service. A task has one known
executor in this service; a broadcast to other services is an event (`docs/guides/mq.md`). **Delayed execution is
only possible here**: events have no delayed delivery.

The code below continues the fictional `Article` domain of `docs/guides/mq.md`: the consumer of "article published"
enqueues a "notify subscribers" task, and a cron task builds a daily report. Replace them with your own.

## Where things go

- **Task contracts** live in `internal/biz/`, next to the UseCase that enqueues them:
  `var Task<Name> = kasynq.NewTaskType[Payload]("<domain>:<action>")`, `struct{}` for no payload
  ([naming](keel:docs/tasks.md#define-a-task)). Enqueuing, handling and
  cron all reference this variable, so a misspelt name or a wrong payload type fails to compile. A task name never
  appears as a bare string in service or server code.
- **Handlers** are service-layer methods `func (h *TaskHandler) HandleXxx(ctx context.Context, p Payload) error`.
- **Registration** is in `internal/server/`: the worker setup (returns `kasynq.WorkerSetup`, called `NewTaskServer`
  below) and the scheduler setup (returns `kasynq.SchedulerSetup`, called `NewSchedulerServer`). If this repository's
  AGENTS.md makes `internal/server/` read-only, put them where AGENTS.md says.
- **Enqueuing** can happen in any entry point; the worker and scheduler run in the entry point that hosts them. An
  entry point that only enqueues assembles only the task client.

## Steps

Adding an asynchronous task:

1. `internal/biz/`: declare the task contract next to the enqueuing UseCase.
2. `internal/service/`: write the handler, a `TaskHandler` method, in `internal/service/task_<name>.go`.
3. `internal/server/`: bind it in `NewTaskServer` with `biz.Task<Name>.Handle(r, handler.HandleXxx)`.
4. The enqueuing UseCase takes a `kasynq.Enqueuer` and calls `biz.Task<Name>.Enqueue(ctx, uc.tasks, payload, ...)`.
5. `di.go`: the enqueuing entry point builds `kasynq.NewTaskClient(redis)`; the worker's entry point builds
   `kasynq.NewWorkerServer(config, redis, workerSetup)`. Done when the process starts and `make check` passes.

Adding a cron task:

1. Contract and handler as above: cron only enqueues the task on a schedule, and the worker must handle it.
2. `internal/server/`: register it in `NewSchedulerServer` with `biz.Task<Name>.RegisterCron(r, cronspec, payload)`.
3. The scheduler's entry point builds `kasynq.NewSchedulerServer(config, redis, schedulerSetup)`. Done when the process
   starts.

**Cron specs are UTC** ([Cron](keel:docs/tasks.md#cron)): `0 3 * * *` is 11:00 in UTC+8. Convert before writing.

## Code

biz: contracts and enqueuing.

```go
// internal/biz/article.go (example)
var (
	// TaskNotifySubscribers is enqueued by the article_published consumer; the worker notifies subscribers.
	TaskNotifySubscribers = kasynq.NewTaskType[ArticlePublishedEvent]("article:notify_subscribers")
	// TaskDailyReport is enqueued by the scheduler; the worker builds the daily report. No payload.
	TaskDailyReport = kasynq.NewTaskType[struct{}]("article:daily_report")
)

// NotifyUseCase depends on the interface kasynq.Enqueuer; a hand-written test double implements its one method.
type NotifyUseCase struct {
	tasks kasynq.Enqueuer
}

func NewNotifyUseCase(tasks kasynq.Enqueuer) *NotifyUseCase {
	return &NotifyUseCase{tasks: tasks}
}

// EnqueueNotify enqueues the notify task for an article_published event.
// Events are delivered at least once, so a redelivery comes here again: a deterministic TaskID from the business ID
// deduplicates it, and Retention keeps the ID taken for a while after the task finishes. Without Retention the ID is
// released as soon as the handler finishes, and "enqueued, finished, process exits before the ack" enqueues it
// twice. A conflict is an idempotent success.
func (uc *NotifyUseCase) EnqueueNotify(ctx context.Context, event *ArticlePublishedEvent) error {
	_, err := TaskNotifySubscribers.Enqueue(ctx, uc.tasks, *event,
		kasynq.MaxRetry(3), kasynq.Queue("default"),
		kasynq.TaskID(fmt.Sprintf("article:notify_subscribers:%d", event.ID)),
		kasynq.Retention(time.Hour))
	if errors.Is(err, kasynq.ErrTaskIDConflict) {
		return nil // an earlier delivery already enqueued it
	}
	if err != nil {
		slog.ErrorContext(ctx, "failed to enqueue notify task",
			"error", err, "article_id", event.ID)
		return errors.Join(ErrUnexpectedError, err)
	}
	return nil
}
```

This deduplicates only within a window (the task's lifetime plus `Retention`). The real safety net is a task whose
business effect is idempotent: sending an already sent notification again must do no harm. The options are listed in
[Enqueue options](keel:docs/tasks.md#enqueue-options).

service: handlers receive the decoded payload. A returned error is retried up to `MaxRetry`; `nil` is success.

```go
// internal/service/task_<name>.go (example)
type TaskHandler struct{}

func NewTaskHandler() *TaskHandler { return &TaskHandler{} }

// HandleNotifySubscribers handles TaskNotifySubscribers.
func (h *TaskHandler) HandleNotifySubscribers(ctx context.Context, event biz.ArticlePublishedEvent) error {
	// Log the ID only, not the user's title: it may contain personal data and task logs are kept long.
	slog.InfoContext(ctx, "notifying subscribers", "article_id", event.ID)
	// The notification itself (mail, SMS, push) goes through a biz UseCase.
	return nil
}

// HandleDailyReport handles a cron task with no payload.
func (h *TaskHandler) HandleDailyReport(ctx context.Context, _ struct{}) error {
	slog.InfoContext(ctx, "executing daily report task")
	return nil
}
```

server: worker and scheduler registration.

```go
// internal/server/ worker registration (example)
func NewTaskServer(handler *service.TaskHandler) kasynq.WorkerSetup {
	return func(r kasynq.HandlerRegistrar) {
		biz.TaskNotifySubscribers.Handle(r, handler.HandleNotifySubscribers)
		biz.TaskDailyReport.Handle(r, handler.HandleDailyReport)
	}
}

// internal/server/ scheduler registration (example). 08:00 UTC is 16:00 in UTC+8.
// Registration errors are returned by kasynq.NewSchedulerServer, so the return values may be dropped here.
func NewSchedulerServer() kasynq.SchedulerSetup {
	return func(r kasynq.ScheduleRegistrar) {
		_, _ = biz.TaskDailyReport.RegisterCron(r, "0 8 * * *", struct{}{})
	}
}
```

The entry point hosting worker and scheduler (`cmd/<app>/di.go`):

```go
redis, redisCleanup, err := kredis.NewRedis(config)
if err != nil {
	return nil, cleanup, err
}
cleanups = append(cleanups, redisCleanup)

taskClient := kasynq.NewTaskClient(redis) // no cleanup: the Redis cleanup above owns the connection

taskHandler := service.NewTaskHandler()
workerServer, err := kasynq.NewWorkerServer(config, redis, server.NewTaskServer(taskHandler))
if err != nil {
	return nil, cleanup, err
}

schedulerServer, err := kasynq.NewSchedulerServer(config, redis, server.NewSchedulerServer())
if err != nil {
	return nil, cleanup, err
}
```

Both are `keel.Server`s and go to `factory.New` in that entry point's `newApp`, with the consumer server of the same
entry point: `factory.New(consumerServer, workerServer, schedulerServer)`.

## Configuration

The worker needs an `asynq.<name>` section in `configs/config.yaml` (concurrency and queue weights); without it the
worker does not start ([Configuration](keel:docs/tasks.md#configuration)). The shutdown budget is
`app.stop_timeout`, not a task setting ([Shutdown](keel:docs/tasks.md#shutdown)): a task that may run close to the
budget either gets a larger `app.stop_timeout` or is safe to run again.

## Long-running loops

A loop that is neither message- nor schedule-driven (a poller, a bot) runs as `worker.Server(runner)`, whose runner
returns on `ctx.Done()` ([Background workers](keel:docs/worker.md)). Never start it as a bare goroutine with
`context.Background()`: the process cannot stop it gracefully.

## Rules for tasks

- **Every cron task has a worker handler**: nothing checks it at registration ([Cron](keel:docs/tasks.md#cron)).
- **Return retryable failures as errors.** `nil` means success and is never retried.
- **Idempotent.** Deduplication by `TaskID` has a window; the task's effect must tolerate running twice.

## Related

- `docs/guides/mq.md`: events across services
- `docs/guides/assembly.md`: `di.go` conventions and cleanup
- [Tasks](keel:docs/tasks.md) and [Background workers](keel:docs/worker.md): the mechanisms
