# Publishing and consuming events (NATS JetStream)

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before publishing a domain event or consuming one (from another service or this one).

What knats does (the stream model, naming, retries, dead letters, delivery guarantees) is in
[Messaging](keel:docs/messaging.md); this guide only covers where each piece goes in this service.

The code below uses a fictional `Article` domain: publishing an article emits an event, and its consumer enqueues a
"notify subscribers" task. The repository need not contain it; replace it with your own domain.

## Event or task

An event is broadcast to subscribers the publisher does not know, possibly in other services; a task has one known
executor in this service. Anything delayed or scheduled is a task, because events have no delayed delivery. The full
comparison is in [Task or event](keel:docs/tasks.md#task-or-event); tasks are in `docs/guides/task.md`.

## Naming in this service

- **One stream per domain**, named after the domain; events are the topics in it. knats adds the `<app.domain>`
  prefix, so write only the topic (`published`), never `<domain>.<stream>.published`
  ([Naming](keel:docs/messaging.md#naming)).
- **Contracts live in `internal/biz/`, next to the task contracts:** `var Stream<Domain> = knats.NewStream("<domain>")`
  and `var Event<Name> = knats.NewEventType[Payload](Stream<Domain>, "<topic>")`. The publisher (biz), the consumer
  (server/service) and `di.go` all start from these variables, so the stream name is written once.
- **`Durable`** is named from this service's point of view (`article_published`). It owns the consumer's progress and
  is unique within the stream.

## Steps

Publishing an event:

1. `internal/biz/`: declare the stream contract if the domain has none, then the event contract (see "Naming").
   Done when it compiles.
2. The UseCase depends on the interface `knats.Publisher` and calls `Event<Name>.Publish(ctx, publisher, payload)`.
3. In the `cmd/<app>/di.go` of every entry point that uses the UseCase: `knats.NewPublisher(natsClient,
   biz.Stream<Domain>, ...)` with every stream this process publishes to, return its error, and pass the publisher to
   the UseCase constructor. When you add a whole new stream, add it here too; a missing one makes publishing return
   `ErrStreamNotDeclared`. Done when the process starts.

Consuming an event:

1. `internal/service/`: write the handler as a method with the signature
   `func(ctx, payload T, d knats.DeliveryInfo) knats.HandlerResult`, `T` being the contract's payload type, in
   `internal/service/consumer_<name>.go`.
2. `internal/server/`: in the consumer registration function (it returns `knats.ConsumerSetup`; called
   `NewConsumerServer` below), call `biz.Event<Name>.Handle(r, handler.Handler, knats.EventOptions{...})` once per
   subscription. If this repository's AGENTS.md makes `internal/server/` read-only, put it where AGENTS.md says.
3. In the `cmd/<app>/di.go` of the entry point that hosts consumers: pass the setup once when creating the consumer
   server (`knats.NewConsumerServer(natsClient, server.NewConsumerServer(handler))`). If no entry point hosts
   consumers yet, add one ("Adding a cmd" in `docs/guides/assembly.md`) or use an existing one.
4. Check that `configs/config.yaml` sets `nats.default.url`. Done when the process starts and `make check` passes.

## Code

Contracts and publishing in biz:

```go
// internal/biz/article.go (example): stream, event and task contracts in one place
var StreamArticle = knats.NewStream("article")
var EventArticlePublished = knats.NewEventType[ArticlePublishedEvent](StreamArticle, "published")

// ArticleUseCase depends on the interface knats.Publisher, so a hand-written test double needs only its methods.
type ArticleUseCase struct {
	repo      ArticleRepo
	publisher knats.Publisher
}

// After the article is stored: publishing is best effort, a failure is logged and does not roll back.
// A business that cannot lose the event needs an outbox (the event stored in the business transaction, sent by a
// separate process).
if err := EventArticlePublished.Publish(ctx, uc.publisher, ArticlePublishedEvent{
	ID:    article.ID,
	Title: article.Title,
}); err != nil {
	slog.ErrorContext(ctx, "failed to publish article_published event",
		"error", err, "article_id", article.ID)
}
```

The publishing entry point (`cmd/<app>/di.go`):

```go
natsClient, natsCleanup, err := knats.NewClient(config)
if err != nil {
	return nil, cleanup, err
}
cleanups = append(cleanups, natsCleanup)
// Every stream this process publishes to; a misconfigured stream stops startup.
publisher, err := knats.NewPublisher(natsClient, biz.StreamArticle)
if err != nil {
	return nil, cleanup, err
}

articleUseCase := biz.NewArticleUseCase(articleRepo, publisher)
```

The handler (service layer). The payload arrives decoded; a message that does not decode never reaches it:

```go
// internal/service/consumer_<name>.go (example)
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-keel/keel/core/knats"

	"<module>/internal/biz"
)

// ArticlePublishedConsumer consumes article_published and enqueues the notify task
// (the usual hand-off from a broadcast event to a point-to-point task).
type ArticlePublishedConsumer struct {
	notify *biz.NotifyUseCase
}

func NewArticlePublishedConsumer(notify *biz.NotifyUseCase) *ArticlePublishedConsumer {
	return &ArticlePublishedConsumer{notify: notify}
}

func (h *ArticlePublishedConsumer) Handler(ctx context.Context, event biz.ArticlePublishedEvent, d knats.DeliveryInfo) knats.HandlerResult {
	// A payload of the right type is not necessarily valid (`{}` decodes too): check its values at this boundary.
	if event.ID <= 0 { // retrying will not make it valid; the error is the dead letter's reason
		return knats.Failed(fmt.Errorf("invalid article_published event: article_id %d", event.ID))
	}

	if err := h.notify.EnqueueNotify(ctx, &event); err != nil {
		slog.ErrorContext(ctx, "failed to handle article_published event",
			"error", err, "article_id", event.ID, "retry_attempts", d.RetryAttempts)
		return knats.RetryLater(err)
	}

	slog.InfoContext(ctx, "article_published event handled", "article_id", event.ID)
	return knats.OK()
}
```

Registration (server layer, no business logic):

```go
// internal/server/ consumer registration (example)
package server

import (
	"github.com/go-keel/keel/core/knats"

	"<module>/internal/biz"
	"<module>/internal/service"
)

func NewConsumerServer(handler *service.ArticlePublishedConsumer) knats.ConsumerSetup {
	return func(r knats.ConsumerRegistrar) {
		biz.EventArticlePublished.Handle(r, handler.Handler, knats.EventOptions{
			Durable: "article_published",
			Workers: 3,
		})
		// More subscriptions are more Handle calls; errors are returned by knats.NewConsumerServer.
	}
}
```

The consuming entry point (`cmd/<app>/di.go`):

```go
natsClient, natsCleanup, err := knats.NewClient(config)
if err != nil {
	return nil, cleanup, err
}
cleanups = append(cleanups, natsCleanup)

// biz → service → server registration (taskClient: see docs/guides/task.md)
notifyUseCase := biz.NewNotifyUseCase(taskClient)
articlePublishedConsumer := service.NewArticlePublishedConsumer(notifyUseCase)
consumerServer, err := knats.NewConsumerServer(natsClient, server.NewConsumerServer(articlePublishedConsumer))
if err != nil {
	return nil, cleanup, err
}
```

`consumerServer` goes to `factory.New` in that entry point's `newApp`, together with any worker and scheduler of the
same entry point: `factory.New(consumerServer, workerServer, schedulerServer)`. When you remove the last
subscription, remove the consumer server and the connection built only for it from `di.go`, because a consumer
server with no subscription fails to start (`ErrNoConsumers`).

## Rules for handlers

- **Idempotent.** Delivery is at least once and order is not guaranteed
  ([Duplicates and ordering](keel:docs/messaging.md#duplicates-and-ordering)). Order-dependent logic decides by a
  version or timestamp in the payload.
- **Retries are bounded by knats.** `knats.RetryLater(err)` is redelivered on a backoff table up to `MaxDeliver`;
  then, or on `knats.Failed(err)`, the message becomes a dead letter that can be replayed
  ([Handler results, retries and dead letters](keel:docs/messaging.md#handler-results-retries-and-dead-letters)).
  Return `knats.Failed(err)` for what retrying cannot fix (an invalid payload); the error is the dead letter's reason.
- **Short.** Slow work becomes a task, or the message is redelivered and processed twice
  ([Slow work goes to a task](keel:docs/messaging.md#slow-work-goes-to-a-task)). An event that enqueues a task
  deduplicates it with a deterministic `kasynq.TaskID` from the business ID (see the code in `docs/guides/task.md`).
- **Layered.** The handler is in the service layer: it calls a biz UseCase and never touches the database or Redis
  (layering in `docs/practices/structure.md`).
- **Not for this service's own asynchronous or delayed work.** That has one known executor: use a task.

## Related

- `docs/guides/task.md`: asynq tasks and cron
- `docs/guides/assembly.md`: `di.go` conventions and cleanup
- [Messaging](keel:docs/messaging.md): knats itself (stream adoption, shutdown, tracing, raw NATS)
