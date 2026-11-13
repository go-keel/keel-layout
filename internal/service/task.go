package service

import (
	"context"
	"log/slog"

	"github.com/go-keel/keel-layout/internal/biz"
)

// TaskHandler handles async tasks.
type TaskHandler struct{}

func NewTaskHandler() *TaskHandler {
	return &TaskHandler{}
}

// Example: delete after forking
//
// HandleSendReminder handles the TaskSendReminder task. TaskType decodes the payload before passing it in;
// the framework archives a payload it cannot decode, and it never reaches here.
func (h *TaskHandler) HandleSendReminder(ctx context.Context, event biz.TodoCreatedEvent) error {
	// Log only the ID, not the title: user input may contain PII, and task logs are kept for a long time.
	// Same principle as "request/response bodies are not logged by default".
	slog.InfoContext(ctx, "sending todo reminder", "todo_id", event.ID)

	// Business logic that actually sends the reminder (email, SMS, push and so on)

	return nil
}

// Example: delete after forking
//
// HandleDailyReport handles the scheduled TaskDailyReport task (no payload).
func (h *TaskHandler) HandleDailyReport(ctx context.Context, _ struct{}) error {
	slog.InfoContext(ctx, "executing daily report task")

	// Business logic of the scheduled task (aggregation, cleanup, sending the report and so on)

	return nil
}
