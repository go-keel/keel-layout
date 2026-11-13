package main

import (
	"github.com/go-keel/keel"
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel-layout/internal/server"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/core/kasynq"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/core/knats"
	"github.com/go-keel/keel/core/kotel"
	"github.com/go-keel/keel/core/kredis"
)

// wireApp assembles dependencies by hand: construct in dependency order, release cleanups in reverse (LIFO).
//
// The parameter is `_`: this entry point reads no field of app.yaml.
// All three entry points keep the same wireApp signature; give `_` a name when you need it.
func wireApp(_ *kconfig.Dynamic[conf.Bootstrap]) (*keel.App, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	config, err := kconfig.NewConfig()
	if err != nil {
		return nil, cleanup, err
	}

	otelCleanup, err := kotel.Setup(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, otelCleanup)

	keel.SetupLogger(config)
	appFactory := keel.NewAppFactory(keel.NewConfig(config))

	natsClient, natsCleanup, err := knats.NewClient(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, natsCleanup)

	redis, redisCleanup, err := kredis.NewRedis(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, redisCleanup)

	// The redis lifetime belongs to the cleanup on the line above; the enqueue client owns nothing to close.
	taskClient := kasynq.NewTaskClient(redis)

	// biz → service → server register
	reminderUseCase := biz.NewReminderUseCase(taskClient)
	todoCreatedConsumer := service.NewTodoCreatedConsumer(reminderUseCase)
	// All subscriptions are registered at once when the consumer server is built.
	consumerServer, err := knats.NewConsumerServer(natsClient, server.NewConsumerServer(todoCreatedConsumer))
	if err != nil {
		return nil, cleanup, err
	}

	taskHandler := service.NewTaskHandler()
	workerSetup := server.NewTaskServer(taskHandler)
	workerServer, err := kasynq.NewWorkerServer(config, redis, workerSetup)
	if err != nil {
		return nil, cleanup, err
	}

	schedulerSetup := server.NewSchedulerServer()
	schedulerServer, err := kasynq.NewSchedulerServer(config, redis, schedulerSetup)
	if err != nil {
		return nil, cleanup, err
	}

	app, err := newApp(appFactory, consumerServer, workerServer, schedulerServer)
	if err != nil {
		return nil, cleanup, err
	}
	return app, cleanup, nil
}
