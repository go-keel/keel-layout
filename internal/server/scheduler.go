package server

import (
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/core/kasynq"
)

// NewSchedulerSetup registers the cron jobs and returns a SchedulerSetup for assembly in di.go.
// Cron specs are read in UTC: `0 8 * * *` is 08:00 UTC, 16:00 in UTC+8.
func NewSchedulerSetup() kasynq.SchedulerSetup {
	return func(r kasynq.ScheduleRegistrar) {
		// The entry ID is only for unregistering at run time, and an encoding error already fails the scheduler's build.
		_, _ = biz.TaskDailyReport.RegisterCron(r, "0 8 * * *", struct{}{})
	}
}
