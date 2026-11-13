// Example file: delete the whole file after forking

package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/core/knats"
)

// TodoCreatedConsumer consumes todo_created events and enqueues reminder tasks
// (the typical hand-off from an event broadcast to a point-to-point async task).
type TodoCreatedConsumer struct {
	reminder *biz.ReminderUseCase
}

func NewTodoCreatedConsumer(reminder *biz.ReminderUseCase) *TodoCreatedConsumer {
	return &TodoCreatedConsumer{reminder: reminder}
}

// Handler handles the event contract biz.EventTodoCreated: the framework has already decoded the payload and Termed
// malformed messages before they reach here (see knats.EventType.Handle), so there is no JSON error to check again.
func (h *TodoCreatedConsumer) Handler(ctx context.Context, event biz.TodoCreatedEvent, d knats.DeliveryInfo) knats.HandlerResult {
	// A payload of the right type is not necessarily valid: `{}` decodes fine, and letting it through enqueues a task
	// for ID=0. Treat an invalid event like a malformed one: retrying will not help, so give up; the reason reaches the
	// give-up log and the dead letter.
	if event.ID <= 0 {
		return knats.Failed(fmt.Errorf("invalid todo_created event: todo_id %d", event.ID))
	}

	if err := h.reminder.EnqueueReminder(ctx, &event); err != nil {
		slog.ErrorContext(ctx, "failed to handle todo_created event",
			"error", err, "todo_id", event.ID, "retry_attempts", d.RetryAttempts)
		// Retry: Keel caps deliveries (ConsumerOptions.MaxDeliver, default 4) and records the give-up as a dead letter.
		return knats.RetryLater(err)
	}

	slog.InfoContext(ctx, "todo_created event handled", "todo_id", event.ID)
	return knats.OK()
}
