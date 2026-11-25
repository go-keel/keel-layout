package server

import (
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/core/kasynq"
)

// NewWorkerSetup registers the async task handlers and returns a WorkerSetup for assembly in di.go.
func NewWorkerSetup(handler *service.TaskHandler) kasynq.WorkerSetup {
	return func(r kasynq.HandlerRegistrar) {
		biz.TaskSendReminder.Handle(r, handler.HandleSendReminder)
		biz.TaskDailyReport.Handle(r, handler.HandleDailyReport)
	}
}
