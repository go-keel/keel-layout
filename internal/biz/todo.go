// Example file: delete the whole file after forking

package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/knats"
)

// Stream, event and task contracts live together: the stream/subject (or task name) is bound to its payload type
// in one place. The producer (biz), the consumer (server/service), cron registration and the publisher built in
// di.go all start from here, so each name is written once. A misspelt variable or a mismatched payload type fails
// to compile; referencing another contract of the same type is not caught.
var (
	// StreamTodo is the event stream of the todo domain; a process that publishes events passes it to
	// knats.NewPublisher in di.go, which validates it at startup.
	StreamTodo = knats.NewStream("todo")
	// EventTodoCreated is published after CreateTodo succeeds; the todo_created consumer subscribes to it.
	EventTodoCreated = knats.NewEventType[TodoCreatedEvent](StreamTodo, "created")

	// TaskSendReminder is enqueued by the todo_created consumer; the worker sends the reminder asynchronously.
	TaskSendReminder = kasynq.NewTaskType[TodoCreatedEvent]("todo:send_reminder")
	// TaskDailyReport is enqueued on a schedule by the scheduler; the worker builds the daily report.
	// It has no payload; to add fields, replace struct{} with a named type.
	TaskDailyReport = kasynq.NewTaskType[struct{}]("todo:daily_report")
)

// Todo is the domain object (DO): a pure business model with no proto and no storage tags.
type Todo struct {
	ID         int64
	Title      string
	Content    string
	Completed  bool
	CreateTime time.Time
	UpdateTime time.Time
}

// TodoCreatedEvent is the payload of the todo_created event.
type TodoCreatedEvent struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// TodoSort is the optional sort of the list. The zero value is the server default; data accepts only the values listed here.
type TodoSort string

const (
	TodoSortDefault        TodoSort = ""
	TodoSortUpdateTimeDesc TodoSort = "update_time_desc"
)

// ListOptions are the list query options; the data layer translates them into the storage query language.
type ListOptions struct {
	// Completed, when non-nil, filters by completion status.
	Completed *bool
	Sort      TodoSort
	Offset    int
	// Limit <= 0 is replaced with the default by ListTodo; the upper bound is enforced by transport validation, not clamped here.
	Limit int
}

// Default page sizes. The upper bound is not in code: it is the lte in the proto, and the transport rejects values beyond it.
const (
	defaultPageSize = 20
	// An export takes more rows at once than a page, so it has its own default.
	defaultExportLimit = 100
)

// Field length limits, counted in **characters**, not bytes. They match the protovalidate constraints of
// CreateTodoRequest in api/.../todo.proto, but **must also exist here**: the proto check covers only the Create DTO,
// while Update takes the Todo message (unconstrained) plus a FieldMask and gets around it. Domain invariants belong
// to the domain layer, and both entry points share the same check.
const (
	maxTitleLen   = 200
	maxContentLen = 2000
)

// RuneCountInString rather than len: protovalidate's string.max_len and MySQL's VARCHAR(200) both count characters.
// Counting bytes would reject 200 Chinese characters (600 bytes) as too long: the proto accepts them and the
// database can store them, yet the domain layer refuses.
func validateTitle(title string) error {
	if title == "" || utf8.RuneCountInString(title) > maxTitleLen {
		return ErrInvalidArgument
	}
	return nil
}

func validateContent(content string) error {
	if utf8.RuneCountInString(content) > maxContentLen {
		return ErrInvalidArgument
	}
	return nil
}

// validateTodo validates a whole Todo, for full-field entry points such as Create.
func validateTodo(todo *Todo) error {
	if todo == nil {
		return ErrInvalidArgument
	}
	if err := validateTitle(todo.Title); err != nil {
		return err
	}
	return validateContent(todo.Content)
}

// Updatable field names. They match the proto FieldMask paths and are the single definition of the contract between
// the caller (service) and the repository (data); data maps them to its own column names.
const (
	FieldTitle     = "title"
	FieldContent   = "content"
	FieldCompleted = "completed"
)

// TodoRepo is the repo interface: biz declares it, data implements it (the dependency-inversion seam).
type TodoRepo interface {
	Create(ctx context.Context, todo *Todo) (*Todo, error)
	FindByID(ctx context.Context, id int64) (*Todo, error)
	// List returns the rows of this page and the total match count: a paged list needs total to compute the page
	// count, without it the caller shows a single page. Count in the database, not len() over the whole table.
	List(ctx context.Context, opts ListOptions) ([]*Todo, int64, error)
	// Update writes only the fields listed in fields and leaves other columns alone. A partial update must be a single
	// UPDATE: reading, merging in memory and writing the whole row back overwrites a concurrent change to other fields.
	Update(ctx context.Context, todo *Todo, fields []string) (*Todo, error)
	Delete(ctx context.Context, id int64) error
}

// TodoUseCase orchestrates the Todo domain logic.
type TodoUseCase struct {
	repo      TodoRepo
	publisher knats.Publisher
}

func NewTodoUseCase(repo TodoRepo, publisher knats.Publisher) *TodoUseCase {
	return &TodoUseCase{repo: repo, publisher: publisher}
}

// CreateTodo stores the todo and publishes the todo_created event.
//
// A failed publish does not roll back the create; it only logs an ERROR. The template shows best-effort publishing
// and accepts losing the event (one reminder fewer, no damage to the main data). A business that cannot accept the
// loss needs an outbox (store the event in the business transaction, then send it from a separate process); this
// template does not show one.
func (uc *TodoUseCase) CreateTodo(ctx context.Context, todo *Todo) (*Todo, error) {
	if err := validateTodo(todo); err != nil {
		return nil, err
	}

	created, err := uc.repo.Create(ctx, todo)
	if err != nil {
		return nil, err
	}

	if err := EventTodoCreated.Publish(ctx, uc.publisher, TodoCreatedEvent{
		ID:    created.ID,
		Title: created.Title,
	}); err != nil {
		slog.ErrorContext(ctx, "failed to publish todo_created event",
			"error", err, "todo_id", created.ID)
	}
	return created, nil
}

func (uc *TodoUseCase) GetTodo(ctx context.Context, id int64) (*Todo, error) {
	return uc.repo.FindByID(ctx, id)
}

// ListTodo returns a paged list; opts.Limit <= 0 uses the default page size.
func (uc *TodoUseCase) ListTodo(ctx context.Context, opts ListOptions) ([]*Todo, int64, error) {
	// Negative counts as "not given" too: gorm treats a negative Limit as unlimited.
	if opts.Limit <= 0 {
		opts.Limit = defaultPageSize
	}
	return uc.repo.List(ctx, opts)
}

// ExportTodo takes the first limit rows for export; limit <= 0 uses the export default.
func (uc *TodoUseCase) ExportTodo(ctx context.Context, limit int) ([]*Todo, error) {
	if limit <= 0 {
		limit = defaultExportLimit
	}
	todos, _, err := uc.repo.List(ctx, ListOptions{Limit: limit})
	return todos, err
}

// UpdateTodo is a partial update: it overwrites only the fields listed in fields.
//
// The field set must travel all the way to data, which issues a single UPDATE. Reading the whole row here, merging
// and writing it back means that when two concurrent PATCHes change title and completed, the later one overwrites
// the earlier, while the update_mask contract is "fields not listed stay unchanged".
func (uc *TodoUseCase) UpdateTodo(ctx context.Context, todo *Todo, fields []string) (*Todo, error) {
	if todo == nil || todo.ID <= 0 || len(fields) == 0 {
		return nil, ErrInvalidArgument
	}
	// The field-name allowlist is enforced here: data only ever receives names it knows.
	for _, f := range fields {
		var err error
		switch f {
		case FieldTitle:
			err = validateTitle(todo.Title)
		case FieldContent:
			err = validateContent(todo.Content)
		case FieldCompleted:
		default:
			err = ErrInvalidArgument
		}
		if err != nil {
			return nil, err
		}
	}
	return uc.repo.Update(ctx, todo, fields)
}

func (uc *TodoUseCase) DeleteTodo(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalidArgument
	}
	return uc.repo.Delete(ctx, id)
}

// ReminderUseCase shows event consumption handing off to an async task (worker side).
type ReminderUseCase struct {
	tasks kasynq.Enqueuer
}

func NewReminderUseCase(tasks kasynq.Enqueuer) *ReminderUseCase {
	return &ReminderUseCase{tasks: tasks}
}

// EnqueueReminder enqueues the reminder task after receiving a todo_created event.
//
// Events are delivered at least once: if the process exits after the task is enqueued but before the event is
// acked, the same event is redelivered here. A deterministic TaskID built from the Todo ID makes a duplicate enqueue
// hit the same task and end as an idempotent success. Retention keeps the ID taken for an hour after the task
// completes; without it the ID is freed as soon as the handler finishes, and the window "enqueue, run, exit before
// ack" still enqueues twice.
//
// This is still **bounded-window** deduplication (it covers the redelivery period, not an extreme replay after an
// hour); the real safety net is that sending the reminder is idempotent: re-sending one must be harmless.
func (uc *ReminderUseCase) EnqueueReminder(ctx context.Context, event *TodoCreatedEvent) error {
	_, err := TaskSendReminder.Enqueue(ctx, uc.tasks, *event,
		kasynq.MaxRetry(3), kasynq.Queue("default"),
		kasynq.TaskID(fmt.Sprintf("todo:send_reminder:%d", event.ID)),
		kasynq.Retention(time.Hour))
	if errors.Is(err, kasynq.ErrTaskIDConflict) {
		slog.InfoContext(ctx, "reminder task already enqueued", "todo_id", event.ID)
		return nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "failed to enqueue reminder task",
			"error", err, "todo_id", event.ID)
		return errors.Join(ErrUnexpectedError, err)
	}
	slog.InfoContext(ctx, "reminder task enqueued", "todo_id", event.ID)
	return nil
}
