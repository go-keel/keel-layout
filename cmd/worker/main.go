package main

import (
	_ "go.uber.org/automaxprocs"

	"github.com/go-keel/keel"
	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/kconfig"
)

func main() {
	bootstrap, cleanup, err := kconfig.NewDynamic[conf.Bootstrap]("configs/app.yaml")
	if err != nil {
		panic(err)
	}
	defer cleanup()

	// Register cleanup before checking the error: in the reverse order a failed start leaks resources.
	// Reason: docs/guides/assembly.md, section "The cmd's main.go skeleton".
	app, appCleanup, err := wireApp(bootstrap)
	if appCleanup != nil {
		defer appCleanup()
	}
	if err != nil {
		panic(err)
	}

	if err = app.Run(); err != nil {
		panic(err)
	}
}

func newApp(
	factory *keel.AppFactory,
	consumerServer keel.Server,
	workerServer *kasynq.WorkerServer,
	schedulerServer *kasynq.SchedulerServer,
) (*keel.App, error) {
	return factory.New(consumerServer, workerServer, schedulerServer)
}
