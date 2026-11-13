# Assembly (di.go, a new cmd, a new infrastructure dependency)

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before changing any `cmd/<app>/di.go`, adding a cmd entry point, or bringing in a new infrastructure
dependency (database, Redis, NATS, asynq, snowflake IDs, distributed lock, ...).

Assembly is written by hand and follows wire's rules: the three constructor shapes, the `kotel.Setup` exception, and
the five rules with their reasons are in `keel:docs/assembly.md#the-five-rules`. Write the data, biz and service
constructors the same way, so the whole program is one dependency graph. This guide only covers how that lands here.

A repository can have one entry point or several, split by transport and job (for example HTTP, gRPC, and event
consumers plus tasks). `internal/{biz,data,service}` is shared by all of them, and **each `cmd/<app>/di.go` assembles
only what that entry point needs**. How many entry points and how to split them is up to each repository.

## Steps

### Adding a dependency to an existing di.go

1. Find its place in the order: a dependency is built before everything that uses it (see "Fixed construction order").
2. Call its constructor in `wireApp` in `cmd/<app>/di.go`; when it returns a cleanup, append it **at once**:
   `cleanups = append(cleanups, xxxCleanup)`.
3. Pass the instance to the data, biz or service constructors that need it.
4. Assemble it only in the entry points that use it: a resource an entry point does not need does not appear in its
   `di.go`.

Done when `make check` passes.

### Fixed construction order

```
kconfig.NewConfig
  → kotel.Setup (process facility; its cleanup flushes)
  → keel.SetupLogger (no result; afterwards use package-level slog)
  → keel.NewAppFactory
  → infrastructure as needed: kdb.NewDB / kredis.NewRedis / knats.NewClient / kasynq.NewTaskClient (needs redis)
  → data (repo implementations) → biz (use cases) → service → server register
  → newApp(factory, ...) → factory.New(servers...)
```

No constructor takes an observability parameter; process facilities stay out of the graph
(`keel:docs/assembly.md#process-facilities-stay-out-of-the-graph`).

Ordering constraints:

- `kasynq.NewTaskClient(redis)` comes after `kredis.NewRedis`. It returns only the client: Redis's lifetime belongs to
  the `kredis.NewRedis` cleanup.
- Event subscriptions are registered all at once through `knats.ConsumerSetup` when the consumer server is built
  (`knats.NewConsumerServer(client, setup)`): `setup` is a parameter of that step, so there is no order between them.
- Snowflake IDs: one generator per process, built once and injected into every use case that issues IDs. Pick by
  `app.node_id`: a fixed value uses `ksnowflake.NewStatic(config)` (no Redis); `-1` uses
  `ksnowflake.NewAuto(config, redis)`, which then comes after `kredis.NewRedis`
  (keel:docs/utilities.md#snowflake-ids-coreksnowflake).

### Adding a cmd

Using cmd/cron as the example:

1. Create cmd/cron/ with `main.go` (load the Bootstrap from `configs/app.yaml` → `wireApp` → `app.Run()`) and
   `di.go` (the `wireApp` skeleton).
2. If it needs a new kind of server, first write its register function (such as `NewSchedulerServer`): registration
   only, no business logic. It goes in `internal/server/` unless this repository's AGENTS.md marks `internal/server/`
   as a read-only kernel; then it goes in the cmd's own directory.
3. Reuse the shared layers: `di.go` assembles only the parts of `internal/{biz,data,service}` this cmd uses. A cmd
   using `kdb` blank-imports `github.com/go-keel/keel/core/kdb/mysql` or `.../core/kdb/postgres` in `main.go`, matching
   the configured `driver`.
4. Add a build target to the `Makefile`:

   ```make
   .PHONY: build-cron
   build-cron:
   	mkdir -p bin/ && $(GO_BUILD) -o ./bin/cron ./cmd/cron
   ```

   Use the shared `GO_BUILD` flags, not a second set. To build it with `make build`, add `build-cron` to the
   dependencies of `build`.
5. If the new cmd needs its own server port, add a named instance under `server.http` / `server.grpc` in
   `configs/config.yaml`.

Done when `make build` produces `bin/cron` and `make check` passes.

## Code patterns

### The wireApp skeleton (cleanups LIFO)

An entry point with HTTP endpoints that publishes events; `<module>` is this repository's module path from `go.mod`,
and `Article` is an example resource. With several entry points, long identical stretches across the `cmd/<app>/di.go`
files are intended: Keel provides no ProviderSet ([keel docs/assembly.md](keel:docs/assembly.md)), so do not fold
them into a shared skeleton.

```go
// cmd/<app>/di.go
package main

import (
	"github.com/go-keel/keel"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/core/kdb"
	"github.com/go-keel/keel/core/knats"
	"github.com/go-keel/keel/core/kotel"

	"<module>/internal/biz"
	"<module>/internal/conf"
	"<module>/internal/data"
	"<module>/internal/server"
	"<module>/internal/service"
)

// wireApp assembles the dependencies in order; cleanups run in reverse (LIFO).
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

	// Only in entry points that publish events (see the mq guide); others do not import knats.
	natsClient, natsCleanup, err := knats.NewClient(config)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, natsCleanup)
	// Every stream this process publishes to, each checked: a misconfigured stream stops startup.
	publisher, err := knats.NewPublisher(natsClient, biz.StreamArticle)
	if err != nil {
		return nil, cleanup, err
	}

	// data → biz → service → server register (these constructors return (T) or (T, error) only)
	articleRepo := data.NewArticleRepo(db)
	articleUseCase := biz.NewArticleUseCase(articleRepo, publisher)
	articleService := service.NewArticleService(articleUseCase)
	httpServerRegister := server.NewHTTPServer(articleService)
	sentinelGroup := service.NewErrorSentinel()

	app, err := newApp(appFactory, httpServerRegister, sentinelGroup)
	if err != nil {
		return nil, cleanup, err
	}
	return app, cleanup, nil
}
```

Every error branch returns `nil, cleanup, err`, never a nil cleanup: whatever was already built must be releasable by
the caller.

### The cmd's main.go skeleton

```go
// cmd/<app>/main.go
package main

import (
	_ "go.uber.org/automaxprocs"

	"github.com/go-keel/keel"
	"github.com/go-keel/keel/core/kconfig"

	"<module>/internal/conf"
)

func main() {
	bootstrap, cleanup, err := kconfig.NewDynamic[conf.Bootstrap]("configs/app.yaml")
	if err != nil {
		panic(err)
	}
	defer cleanup()

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
```

**Register `appCleanup` before checking the error, never after.** Every error branch of `wireApp` returns a usable
cleanup holding what was already built (otel, db, nats, ...). Deferred after the check, the `panic` on a failed start
comes first and those resources are never released; the costliest loss is the unflushed OTel traces and logs, which
are exactly the data that explains why startup failed. `panic` runs deferred calls already registered, so registering
first saves them. The nil check covers failures so early that no cleanup exists yet.

`newApp` also lives in `main.go`: it takes `factory.Config()`, builds the chain from `mw.Default(c.Config)` and the
`mw.With*` presets (authentication is not mounted: the server builds it from its own config, see
`docs/guides/auth.md`), constructs the servers that need the chain, and ends with `factory.New(servers...)`:

```go
// cmd/<app>/main.go: an entry point with an HTTP server
func newApp(
	factory *keel.AppFactory,
	apiRegister api.HTTPServerRegister,
	streamRegister api.StreamRegister, // only when there are raw routes; see the raw-http guide
	sg *sentinel.Group,
) (*keel.App, error) {
	c := factory.Config()

	chain := mw.Default(c.Config)
	mw.WithLocale(chain, i18n.NewBundle())
	mw.WithSentinel(chain, sg)

	apiServer, err := api.NewHTTPServer(c.Server.Http["default"], apiRegister, chain, streamRegister)
	if err != nil {
		return nil, err
	}
	return factory.New(apiServer)
}
```

A gRPC server has the same shape (`api.NewGRPCServer(c.Server.Grpc["default"], register, chain)`). Servers that need
no chain (consumer, worker, scheduler) are built in `wireApp`; `newApp` takes them as ready `keel.Server` values and
calls `factory.New(consumerServer, workerServer, schedulerServer)`. Chain presets and inserting your own middleware:
`keel:docs/middleware.md#building-a-chain`.

### Named instances

A second database, Redis or NATS uses the `Named` constructor with the key of its config map entry:

```go
// reads database.analytics in config.yaml
analyticsDB, dbCleanup, err := kdb.NewNamedDB(config, "analytics")

// reads redis.cache
cacheRedis, redisCleanup, err := kredis.NewNamedRedis(config, "cache")

// reads nats.events
eventClient, natsCleanup, err := knats.NewNamedClient(config, "events")
```

The task client has no `Named` variant: `kasynq.NewTaskClient(rdb)` ignores the `asynq.<name>` sections, and the
Redis it uses is the connection passed in. `NewDB`, `NewRedis` and `NewClient` mean the name `default`.

```yaml
# configs/config.yaml
database:
  default:
    driver: mysql
    source: user:pass@tcp(localhost:3306)/main?parseTime=True&loc=UTC&time_zone=%27%2B00%3A00%27
  analytics:
    driver: mysql
    source: user:pass@tcp(localhost:3306)/analytics?parseTime=True&loc=UTC&time_zone=%27%2B00%3A00%27

redis:
  default:
    addr: ["127.0.0.1:6379"]
  cache:
    addr: ["127.0.0.1:6380"]
```

### Extra components (snowflake IDs, distributed lock)

A teaching example: `Article` and `biz.NewArticleUseCase` only show the order. Build them explicitly in `wireApp` and
inject them downstream:

```go
import (
	"github.com/go-keel/keel/core/kredis/lock"
	"github.com/go-keel/keel/core/ksnowflake"
)

// Snowflake IDs: one generator for the whole process.
// With a fixed app.node_id use ksnowflake.NewStatic(config), no Redis needed;
// with -1 use the one below, after kredis.NewRedis.
ids, idsCleanup, err := ksnowflake.NewAuto(config, redis)
if err != nil {
	return nil, cleanup, err
}
cleanups = append(cleanups, idsCleanup)

// Distributed lock, on Redis. How to use it is in docs/guides/domain-model.md; this only shows assembly.
locker := lock.NewLocker(redis)

// Inject into the biz or data constructors that need them
articleUseCase := biz.NewArticleUseCase(articleRepo, ids, locker)
```

## Anti-patterns

- ❌ `return nil, nil, err` in an error branch: the cleanup is lost and the DB, Redis and OTel connections already open
  leak. Always return `cleanup`.
- ❌ Forgetting `cleanups = append(cleanups, xxxCleanup)` after a successful constructor, or running cleanups in
  order: they must run LIFO, last built first closed.
- ❌ Calling `kconfig.NewConfig` or opening connections inside biz, data or service: all infrastructure is built in
  `di.go` and injected through constructor parameters.
- ❌ Injecting a `*slog.Logger`: `keel.SetupLogger` already set the default logger; call `slog.InfoContext(ctx, ...)`.
- ❌ Every cmd assembling every resource: components an entry point does not use (db, redis, nats, asynq) are neither
  imported nor built there.
- ❌ More than one snowflake generator in a process: generators with the same node ID issue the same IDs in the same
  millisecond. Build one in `di.go` and inject it everywhere.
- ❌ Route or handler registration written in `di.go`: registration goes in named register functions (by default in
  `internal/server/`); `di.go` only connects them.

## Related

- Config structure and Bootstrap reload: `docs/guides/config.md`
- Domain errors and sentinel (enabled in `newApp`): `docs/guides/errors.md`
- `keel:docs/assembly.md`: the five rules and their reasons
- `keel:docs/architecture.md`: the layers these constructors belong to
