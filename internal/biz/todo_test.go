// Example file: delete the whole file after forking

package biz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/knats"
)

// fakeTodoRepo is a hand-written fake: biz tests depend only on the repo interface and need no infrastructure.
type fakeTodoRepo struct {
	todos  map[int64]*Todo
	nextID int64
}

var _ TodoRepo = (*fakeTodoRepo)(nil)

func newFakeTodoRepo() *fakeTodoRepo {
	return &fakeTodoRepo{todos: map[int64]*Todo{}, nextID: 1}
}

func (f *fakeTodoRepo) Create(_ context.Context, todo *Todo) (*Todo, error) {
	created := *todo
	created.ID = f.nextID
	f.nextID++
	f.todos[created.ID] = &created
	return &created, nil
}

func (f *fakeTodoRepo) FindByID(_ context.Context, id int64) (*Todo, error) {
	todo, ok := f.todos[id]
	if !ok {
		return nil, ErrTodoNotFound
	}
	return todo, nil
}

func (f *fakeTodoRepo) List(_ context.Context, opts ListOptions) ([]*Todo, int64, error) {
	var result []*Todo
	for _, t := range f.todos {
		if opts.Completed != nil && t.Completed != *opts.Completed {
			continue
		}
		result = append(result, t)
	}
	return result, int64(len(result)), nil
}

// Update overwrites only the fields listed in fields: if the fake wrote the whole row back, the contract "fields not
// listed stay unchanged" could not be tested.
func (f *fakeTodoRepo) Update(_ context.Context, todo *Todo, fields []string) (*Todo, error) {
	cur, ok := f.todos[todo.ID]
	if !ok {
		return nil, ErrTodoNotFound
	}
	updated := *cur
	for _, field := range fields {
		switch field {
		case FieldTitle:
			updated.Title = todo.Title
		case FieldContent:
			updated.Content = todo.Content
		case FieldCompleted:
			updated.Completed = todo.Completed
		default:
			return nil, ErrInvalidArgument
		}
	}
	f.todos[todo.ID] = &updated
	return &updated, nil
}

func (f *fakeTodoRepo) Delete(_ context.Context, id int64) error {
	if _, ok := f.todos[id]; !ok {
		return ErrTodoNotFound
	}
	delete(f.todos, id)
	return nil
}

// fakePublisher records published events and implements knats.Publisher, which is what EventType.Publish takes,
// so business tests need no NATS (see keel docs/messaging.md).
type fakePublisher struct {
	published []publishedEvent
	failWith  error
}

type publishedEvent struct {
	stream  knats.Stream
	subject string
}

var _ knats.Publisher = (*fakePublisher)(nil)

func (f *fakePublisher) Publish(_ context.Context, stream knats.Stream, subject string, _ any) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.published = append(f.published, publishedEvent{stream, subject})
	return nil
}

func (f *fakePublisher) PublishEx(_ context.Context, stream knats.Stream, subject string, _ knats.Publishing) error {
	return f.Publish(context.Background(), stream, subject, nil)
}

func TestTodoUseCase_CreateTodo(t *testing.T) {
	repo := newFakeTodoRepo()
	publisher := &fakePublisher{}
	uc := NewTodoUseCase(repo, publisher)

	created, err := uc.CreateTodo(context.Background(), &Todo{Title: "write tests"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected server-assigned ID")
	}
	want := publishedEvent{StreamTodo, "created"}
	if len(publisher.published) != 1 || publisher.published[0] != want {
		t.Fatalf("expected %v published, got %v", want, publisher.published)
	}
}

func TestTodoUseCase_CreateTodo_EmptyTitle(t *testing.T) {
	uc := NewTodoUseCase(newFakeTodoRepo(), &fakePublisher{})

	_, err := uc.CreateTodo(context.Background(), &Todo{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestTodoUseCase_CreateTodo_PublishFailureDoesNotRollback(t *testing.T) {
	repo := newFakeTodoRepo()
	publisher := &fakePublisher{failWith: errors.New("broker down")}
	uc := NewTodoUseCase(repo, publisher)

	created, err := uc.CreateTodo(context.Background(), &Todo{Title: "resilient"})
	if err != nil {
		t.Fatalf("CreateTodo should tolerate publish failure: %v", err)
	}
	if _, err := repo.FindByID(context.Background(), created.ID); err != nil {
		t.Fatalf("todo should be persisted despite publish failure: %v", err)
	}
}

// Create and Update share the same length limit: protovalidate in the proto covers only the Create DTO, Update takes
// the Todo message plus a FieldMask and gets around it, so the domain layer must check itself.
func TestTodoUseCase_FieldLimits(t *testing.T) {
	repo := newFakeTodoRepo()
	uc := NewTodoUseCase(repo, &fakePublisher{})
	ctx := context.Background()

	existing, err := uc.CreateTodo(ctx, &Todo{Title: "ok"})
	if err != nil {
		t.Fatal(err)
	}

	longTitle := strings.Repeat("x", maxTitleLen+1)
	longContent := strings.Repeat("x", maxContentLen+1)

	if _, err := uc.CreateTodo(ctx, &Todo{Title: longTitle}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("CreateTodo title too long: got %v", err)
	}
	if _, err := uc.CreateTodo(ctx, &Todo{Title: "ok", Content: longContent}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("CreateTodo content too long: got %v", err)
	}
	if _, err := uc.UpdateTodo(ctx, &Todo{ID: existing.ID, Title: longTitle}, []string{FieldTitle}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UpdateTodo title too long: got %v", err)
	}
	if _, err := uc.UpdateTodo(ctx, &Todo{ID: existing.ID, Title: "ok", Content: longContent}, []string{FieldTitle, FieldContent}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UpdateTodo content too long: got %v", err)
	}

	// Exactly at the limit must pass; do not write the bound as >=
	if _, err := uc.UpdateTodo(ctx, &Todo{ID: existing.ID, Title: strings.Repeat("x", maxTitleLen)}, []string{FieldTitle}); err != nil {
		t.Fatalf("UpdateTodo exactly at the limit should pass: %v", err)
	}
}

// The limit counts characters, not bytes: protovalidate's max_len and MySQL's VARCHAR(n) both count characters.
// len() would reject 200 Chinese characters (600 bytes) as too long: the proto accepts them and the database can
// store them, yet the domain layer refuses.
func TestTodoUseCase_FieldLimits_CountsRunesNotBytes(t *testing.T) {
	uc := NewTodoUseCase(newFakeTodoRepo(), &fakePublisher{})
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		title string
		ok    bool
	}{
		{"Chinese exactly at the limit", strings.Repeat("中", maxTitleLen), true},
		{"Chinese one character over", strings.Repeat("中", maxTitleLen+1), false},
		{"emoji exactly at the limit", strings.Repeat("🐨", maxTitleLen), true},
		{"emoji one character over", strings.Repeat("🐨", maxTitleLen+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.CreateTodo(ctx, &Todo{Title: tc.title})
			if tc.ok && err != nil {
				t.Fatalf("should pass, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("should reject, got %v", err)
			}
		})
	}
}

func TestTodoUseCase_GetTodo_NotFound(t *testing.T) {
	uc := NewTodoUseCase(newFakeTodoRepo(), &fakePublisher{})

	_, err := uc.GetTodo(context.Background(), 42)
	if !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("expected ErrTodoNotFound, got %v", err)
	}
}

// fakeEnqueuer records enqueued tasks. The fake implements only the single bytes-level Enqueue of kasynq.Enqueuer:
// the framework makes it the point business tests implement, and the typed TaskType.Enqueue lands here through it.
type fakeEnqueuer struct {
	enqueued []string // type names
	failWith error
}

var _ kasynq.Enqueuer = (*fakeEnqueuer)(nil)

func (f *fakeEnqueuer) Enqueue(_ context.Context, typeName string, _ []byte, _ ...kasynq.EnqueueOption) (*kasynq.TaskInfo, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.enqueued = append(f.enqueued, typeName)
	return &kasynq.TaskInfo{}, nil
}

func TestReminderUseCase_EnqueueReminder(t *testing.T) {
	client := &fakeEnqueuer{}
	uc := NewReminderUseCase(client)

	err := uc.EnqueueReminder(context.Background(), &TodoCreatedEvent{ID: 1, Title: "x"})
	if err != nil {
		t.Fatalf("EnqueueReminder: %v", err)
	}
	if len(client.enqueued) != 1 || client.enqueued[0] != "todo:send_reminder" {
		t.Fatalf("expected todo:send_reminder enqueued, got %v", client.enqueued)
	}
}

func TestReminderUseCase_EnqueueReminder_IDConflictIsSuccess(t *testing.T) {
	client := &fakeEnqueuer{failWith: kasynq.ErrTaskIDConflict}
	uc := NewReminderUseCase(client)

	// A redelivered event hits the unfinished task of the same ID: the previous enqueue succeeded, so this one ends as an idempotent success.
	if err := uc.EnqueueReminder(context.Background(), &TodoCreatedEvent{ID: 1}); err != nil {
		t.Fatalf("ID conflict should be idempotent success, got %v", err)
	}
	if len(client.enqueued) != 0 {
		t.Fatalf("conflicting enqueue must not be recorded, got %v", client.enqueued)
	}
}

func TestReminderUseCase_EnqueueReminder_Failure(t *testing.T) {
	client := &fakeEnqueuer{failWith: errors.New("redis down")}
	uc := NewReminderUseCase(client)

	err := uc.EnqueueReminder(context.Background(), &TodoCreatedEvent{ID: 1})
	if !errors.Is(err, ErrUnexpectedError) {
		t.Fatalf("expected ErrUnexpectedError, got %v", err)
	}
}
