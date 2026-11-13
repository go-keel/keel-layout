// Example file: delete the whole file after forking

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/knats"
)

// fakeEnqueuer stands up ReminderUseCase; failWith injects an enqueue failure.
type fakeEnqueuer struct{ failWith error }

var _ kasynq.Enqueuer = (*fakeEnqueuer)(nil)

func (f *fakeEnqueuer) Enqueue(context.Context, string, []byte, ...kasynq.EnqueueOption) (*kasynq.TaskInfo, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &kasynq.TaskInfo{}, nil
}

// How Handler disposes of a message: semantically invalid → Failed (Term), valid event → OK, an enqueue failure →
// RetryLater (Keel caps the deliveries and records the give-up as a dead letter). Bad JSON is not tested
// here: the event contract's Handle Terms it before it reaches this function, see
// TestEventContract_UndecodablePayloadDropped in Keel core/knats.
func TestTodoCreatedConsumer_Handler(t *testing.T) {
	cases := []struct {
		name     string
		event    biz.TodoCreatedEvent
		enqueue  error
		attempts int
		want     string // prefix of the result's String()
	}{
		{"missing id", biz.TodoCreatedEvent{}, nil, 0, "Failed: invalid todo_created event: todo_id 0"},
		{"valid event", biz.TodoCreatedEvent{ID: 42, Title: "x"}, nil, 0, "OK"},
		{"enqueue fails", biz.TodoCreatedEvent{ID: 42}, errors.New("redis down"), 2, "RetryLater: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTodoCreatedConsumer(biz.NewReminderUseCase(&fakeEnqueuer{failWith: tc.enqueue}))
			got := h.Handler(context.Background(), tc.event, knats.DeliveryInfo{RetryAttempts: tc.attempts})
			if !strings.HasPrefix(got.String(), tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
