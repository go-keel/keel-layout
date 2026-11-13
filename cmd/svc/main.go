package main

import (
	_ "go.uber.org/automaxprocs"

	_ "github.com/go-keel/keel/core/kdb/mysql"

	"github.com/go-keel/keel"
	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel-layout/internal/i18n"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/middleware/sentinel"
	"github.com/go-keel/keel/mw"
	"github.com/go-keel/keel/server/api"
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

func newApp(factory *keel.AppFactory, grpcRegister api.GRPCServerRegister, sg *sentinel.Group) (*keel.App, error) {
	c := factory.Config()
	grpcCfg := c.Server.Grpc["default"]

	chain := mw.Default(c.Config)
	mw.WithLocale(chain, i18n.NewBundle())
	mw.WithSentinel(chain, sg)

	grpcServer, err := api.NewGRPCServer(
		grpcCfg,
		grpcRegister,
		chain,
	)
	if err != nil {
		return nil, err
	}

	return factory.New(grpcServer)
}
