package main

import (
	"github.com/go-keel/keel"
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel-layout/internal/data"
	"github.com/go-keel/keel-layout/internal/server"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/core/kdb"
	"github.com/go-keel/keel/core/knats"
	"github.com/go-keel/keel/core/kotel"
)

// wireApp assembles dependencies by hand: construct in dependency order, release cleanups in reverse (LIFO).
func wireApp(bootstrap *kconfig.Dynamic[conf.Bootstrap]) (*keel.App, func(), error) {
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

	db, dbCleanup, err := kdb.NewDB(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, dbCleanup)

	natsClient, natsCleanup, err := knats.NewClient(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, natsCleanup)
	// Pass every stream this process publishes to; each is validated, so a misconfigured stream fails startup.
	publisher, err := knats.NewPublisher(natsClient, biz.StreamTodo)
	if err != nil {
		return nil, cleanup, err
	}

	// data → biz → service → server register
	todoRepo := data.NewTodoRepo(db)
	todoUseCase := biz.NewTodoUseCase(todoRepo, publisher)
	todoService := service.NewTodoService(todoUseCase)
	streamService := service.NewStreamService(todoUseCase)
	webhookHandler := service.NewWebhookHandler(bootstrap)
	httpServerRegister := server.NewHTTPServer(todoService)
	streamRegister := server.NewStreamRegister(streamService, webhookHandler)
	sentinelGroup := service.NewErrorSentinel()

	app, err := newApp(appFactory, httpServerRegister, streamRegister, sentinelGroup)
	if err != nil {
		return nil, cleanup, err
	}
	return app, cleanup, nil
}
