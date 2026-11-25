# Upgrading guide

This file holds entries only, one per change that actually happened; how to write entries and how to follow
upstream: `docs/upgrading-protocol.md`.

## 2026-11-25 · 6

**What changed.** The template follows Keel's one way of doing i18n (Keel's `UPGRADING.md`, "`locale.Bundle` is gone"):
`internal/i18n.NewBundle` returns go-i18n's `*i18n.Bundle` without Keel's wrapper; `MustLocalizeMessage` is gone.
`internal/service/errors.go` maps each error with one `OnMessage` line instead of an `OnLocaled` handler per error.
`internal/i18n/bundle.go` becomes a shared file (`scripts/cross-repo-sync.list`). A `VALIDATOR` error now carries the
validation library's English text naming the field, the same in every language; the errors guide and
`docs/practices/user-text.md` say it is for developers and that callers make sure parameters are valid. The e2e locale
case checks language matching on a domain error instead.

**Why.** Keel now translates with fallback itself (`locale.Localize`, `sentinel.OnMessage`), so the per-error
handlers and the template's own fallback helper were a second way to do it. Field-format checks are the caller's job;
their failures are for the caller's developer.

**How to apply.**

1. Add `keel-layout internal/i18n/bundle.go` to `scripts/cross-repo-sync.list` (with your direct upstream in the
   first field), then take it and the shared guides with `make sync-upstream`. Delete
   `TestMustLocalizeMessage_FallsBackToEnglish` from your `internal/i18n/bundle_test.go`.
2. Replace each `OnLocaled(err, translateXxx)` whose handler only localizes a message into `v1.ErrorXxx` with
   `OnMessage(err, &goi18n.Message{ID: ..., Other: ...}, v1.ErrorXxx)`, moving the ID and English text from the
   handler, and delete the handler. Other handlers become `On(err, handler)` calling `locale.Localize(ctx, msg)` in
   place of `i18n.MustLocalizeMessage`.
3. Set the `goi18n` version in your `Makefile` (`GOI18N`) to the go-i18n version in `go.mod`, then run
   `make i18n-extract`: the extracted English must be unchanged.
4. Tests asserting a translated `VALIDATOR` message: assert the field name instead. Clients that showed it to end
   users: check the input in the client first.

## 2026-11-25 · 5

**What changed.** The template follows Keel v0.2.0 (its `UPGRADING.md`, section "v0.2.0"):
`cmd/*/di.go` no longer builds `keel.NewAppFactory(keel.NewConfig(config))` and passes `config` to `newApp`, which
takes `*kconfig.Config`, builds the chain from `mw.Default(cfg)` and ends with `keel.NewApp(cfg, servers...)`. The
e2e tests build clients with `keel.NewServiceClient(cfg, ...)`, write metadata with `metadata.Set`, give lock TTLs as
`time.Duration` and no longer expect `knats.domain-missing` for an empty `app.domain`; `internal/service/rawhttp_test.go`
uses `metadata.Set`. The shared guides teach the new calls (`keel.NewApp`, `keel.NewServiceClient(config, ...)`,
`metadata.Get` / `metadata.Set`, `*lock.Locker` with `time.Duration` TTLs, `worker.NewServer`) and say "where the
entry point builds its middleware chain" instead of naming `newApp` in `cmd/<app>/main.go`. `go.mod` names
Keel `v0.2.0`, and the optional modules (`core/kasynq`, `core/kdb/mysql`, `core/kdb/postgres`, `core/knats`) the
pseudo-version of that tag's commit, `v0.0.0-20261125211609-9d679e606cb4`.

**Why.** Keel replaced its config wrapper and factories with the loaded `*kconfig.Config`, and changed the metadata,
lock and worker APIs.

**How to apply.**

1. Set the Keel versions in `go.mod` to the ones above, run `go mod tidy`, and apply Keel's own `UPGRADING.md`
   entries between your version and that one; `go build ./...` and `go vet -tags e2e ./...` list the call sites.
2. In each `cmd/<app>/di.go` and `main.go`: delete `appFactory := keel.NewAppFactory(...)`, pass `config` where the
   factory went, replace `factory.Config()` / `c.Config` with the config itself and `factory.New(...)` with
   `keel.NewApp(cfg, ...)`.
3. Multiply every `TryAcquireLock` TTL by `time.Second` (a bare integer still compiles and means nanoseconds).
4. Take the shared guides with `make sync-upstream`.

## 2026-11-25 · 4

**What changed.** The register functions in `internal/server/` are named after the Keel type they return:
`NewHTTPServer` → `NewHTTPServerRegister`, `NewGRPCServer` → `NewGRPCServerRegister`, `NewConsumerServer` →
`NewConsumerSetup`, `NewTaskServer` → `NewWorkerSetup`, `NewSchedulerServer` → `NewSchedulerSetup`
(`NewStreamRegister` is unchanged); the three `cmd/*/di.go` call the new names. The example's biz names carry the
resource: `ListOptions` → `ListTodoOptions`, `FieldTitle` / `FieldContent` / `FieldCompleted` → `TodoFieldTitle` /
`TodoFieldContent` / `TodoFieldCompleted`, `UpdatableFields` → `TodoUpdatableFields`. The shared guides use the new
names and teach `List<Resource>Options`, as `docs/practices/structure.md` already did.

**Why.** The old register names matched Keel's server constructors (`api.NewHTTPServer`, `knats.NewConsumerServer`,
`kasynq.NewSchedulerServer`) while returning something else; unprefixed biz names collide as soon as a second
resource is added.

**How to apply.**

1. Take the shared guides with `make sync-upstream`.
2. In `internal/server/` and the `di.go` files that call them, rename the register functions as above (skip any you
   renamed or removed yourself).
3. Optional: give your own unprefixed list options and field-name constants in `internal/biz/` the resource prefix
   (`List<Resource>Options`, `<Resource>Field<Name>`, `<Resource>UpdatableFields`).

## 2026-11-25 · 3

**What changed.** The `Makefile` no longer sets `VERSION` or passes `-X main.Version=...` to the linker (no `main.go`
declares that variable; the version a service reports is `app.version` in `configs/app.yaml`), and `make generate`
runs only `go mod tidy` (the repository has no `//go:generate` directives). `internal/testdb` exports
`Root(t testing.TB)`, the repository root; `e2e/harness/harness.go` uses it instead of its own `harness.Root`, which
is gone, and so imports `internal/testdb` under this repository's module path: its line in
`scripts/cross-repo-sync.list` becomes a `norm` entry.

**Why.** The linker flag and `go generate` did nothing; the root lookup was written twice in two shared files.

**How to apply.**

1. In your `Makefile`, delete the `VERSION?=` line, ` -X main.Version=$(VERSION)` from `GO_BUILD` and
   `go generate ./...` from `generate`, unless you added a `Version` variable or `//go:generate` directives yourself.
2. Take both shared files with `make sync-upstream` (the `e2e/harness/harness.go` line in
   `scripts/cross-repo-sync.list` ends in ` norm`, as `2026-11-25 · 2` registers it); replace any `harness.Root(t)`
   of your own with `testdb.Root(t)`.

## 2026-11-25 · 2

**What changed.** End-to-end tests arrive: `make e2e` builds `cmd/*`, runs the binaries as processes against the four
test servers and tests them from outside, once per round of the table in `e2e/main_test.go` (template: everything on
MySQL, everything on PostgreSQL). The process work is the shared file `e2e/harness/harness.go`; the layer is described
in `docs/guides/testing.md`, "End-to-end tests". The harness starts each binary with only the committed
`configs/config.yaml` and `configs/app.yaml` and reaches the per-environment fields through `KEEL_` variables, so
`config.yaml` gains placeholders with the old values as defaults: `${LOGGER_FORMAT:text}`, `${HTTP_ADDR:...}`,
`${GRPC_ADDR:...}`, `${OTEL_ENDPOINT:...}`, `${DB_DEFAULT_DRIVER:...}`, `${DB_DEFAULT_SOURCE:...}`,
`${REDIS_ADDR:...}` and `${NATS_URL:...}`. `internal/testdb` gains `Create(t, database, dialect, name)`, which builds
one named database and returns the DSN a service connects with; `harness.Config` reads a value from the committed
config, so cases do not spell out the repository's own names. The Todo example's cases in `e2e/` carry the usual
example markers, and so does starting the worker, which has no server once the example is gone.

**Why.** Unit and narrow integration tests never run the binary that ships; the end-to-end layer checks startup,
config loading, the processes working together (HTTP → event → task) and both databases, instead of a manual test run.

**How to apply.**

1. Add `keel-layout e2e/harness/harness.go norm` to `scripts/cross-repo-sync.list` (with your direct upstream in the
   first field), take it and the new `internal/testdb/testdb.go` with `make sync-upstream`, and copy `e2e/main_test.go` and
   `e2e/helpers_test.go` from upstream, dropping the example parts.
2. Put the same placeholders into your `configs/config.yaml`, keeping your current values as the defaults; one
   `${DB_<NAME>_DRIVER}` and `${DB_<NAME>_SOURCE}` pair per database.
3. Edit `rounds` in `e2e/main_test.go` to the databases and drivers you deploy, and `setup` to the processes you run.
4. Add the `e2e` target from upstream's `Makefile` (it uses `GO_TEST`, added in `2026-11-25 · 1`), set the four
   `TEST_*` variables and run `make e2e`; write cases for
   your own endpoints and flows as you change them.

## 2026-11-25 · 1

**What changed.** Tests come in three layers, one command each: unit tests (`make test`), narrow integration tests
(`make integration`) and end-to-end tests (`make e2e`); the terms are in the shared segment of `GLOSSARY.md` and the
rules in `docs/guides/testing.md`. `make check` runs static checks only, no test, and vets the `integration` and `e2e`
build tags too. `make integration` needs all four of `TEST_MYSQL_DSN`, `TEST_POSTGRES_DSN`, `TEST_REDIS_ADDR` and
`TEST_NATS_URL`, pointing at servers for tests only. The test commands build with `CGO_ENABLED=0`, and add `-race`
only on macOS. The database helper for tests moved from `internal/data/testdb_integration_test.go` to the shared
package `internal/testdb` (build tags `integration || e2e`): `testdb.ForEachDB(t, databases, fn)` hands `fn` a
`*gorm.DB`, and `databases` (`nil` for every database) picks the schema scripts applied. `cmd/api` and `cmd/svc`
blank-import both `kdb/mysql` and `kdb/postgres`, so switching databases is a config change. Tests on Redis use
database 0 only.

**Why.** One command per layer: static checks and unit tests need no server and run before every commit, the slower
layers run when a change calls for them. Real servers instead of emulators, because an emulator differs from the real
server in details.

**How to apply.**

1. From upstream's `Makefile` take the `GO_TEST` variable and the `test` and `integration` targets; in `check`,
   delete the `go test` line and add `GOWORK=off go vet -tags integration ./...` and
   `GOWORK=off go vet -tags e2e ./...`. Wherever only `make check` ran (before commits, in CI), run
   `make check test`.
2. Set `TEST_REDIS_ADDR` and `TEST_NATS_URL` next to the two DSNs for `make integration`, all on servers for tests only.
3. Add `keel-layout internal/testdb/testdb.go` to `scripts/cross-repo-sync.list` (with your direct upstream in the
   first field) and take the file with `make sync-upstream`. Delete `internal/data/testdb_integration_test.go`, and
   in each `*_integration_test.go` replace `forEachDB(t, fn)` with `testdb.ForEachDB(t, nil, fn)`, where `fn` takes a
   `*gorm.DB` and builds its repo with `NewXxxRepo(&kdb.DB{DB: db})`.
4. Add `_ "github.com/go-keel/keel/core/kdb/postgres"` (or `kdb/mysql`) next to the existing driver import in each
   `main.go` that uses `kdb`, then `go mod tidy`; or keep only the driver you deploy.
5. Replace emulators in your own tests (SQLite, miniredis, an embedded NATS server) with the real test servers.

## 2026-11-24 · 1

**What changed.** The template supports MySQL and PostgreSQL alike. Schema scripts moved one level down, into one
directory per dialect: `migrations/<database>/mysql/` and `migrations/<database>/postgres/`, named after the
`database.<name>.driver` value. `internal/data/testdb_integration_test.go` replaces `openTestDB(t)` with
`forEachDB(t, fn)`, which runs a data-layer test once per dialect the repository runs on, each on a fresh database
built from `migrations/*/<dialect>/0_schema.sql`; `make integration` now needs `TEST_POSTGRES_DSN` as well as `TEST_MYSQL_DSN`
while both dialects are kept. PostgreSQL identity columns are `GENERATED BY DEFAULT`, so they accept explicit IDs as
MySQL's `AUTO_INCREMENT` does. The shared docs say how a PostgreSQL script mirrors the MySQL rules, notably the `ci`
collation that keeps text comparisons case- and accent-insensitive on both.

**Why.** Keel's database component supports both databases, but the template only ever ran on MySQL; code that
silently relied on MySQL behaviour went unnoticed. Testing every dialect a repository keeps catches it.

**How to apply.**

1. Move each `migrations/<database>/*.sql` into `migrations/<database>/<dialect>/`, the dialect your database runs.
   Data tests run on the dialects every database directory has, so keep only that one unless you mean to support the
   other too.
2. In each `*_integration_test.go`, run the test once per dialect instead of on `openTestDB(t)`: the helper for that
   is `testdb.ForEachDB`, taken in `2026-11-25 · 1`, step 3 (the `internal/data/testdb_integration_test.go` named
   above was replaced by it).
3. Run `go mod tidy`: the helper imports `gorm.io/driver/postgres`.
4. Set `TEST_POSTGRES_DSN` if you kept the PostgreSQL dialect (docs/guides/testing.md, "data tests") and run
   `make integration`.

## 2026-11-19 · 1

**What changed.** Following upstream no longer reruns `make rename`. `make sync-upstream` writes upstream's shared
files and `norm` kernel trees into this repository, already carrying its module path (the same normalization
`make check-sync` compares with), deletes what upstream deleted in a directory entry, and only reports
shared segments. `make rename` runs once, when the repository is created, and refuses to rename a repository that
already has its own module path.

**Why.** Rerunning the rename over the whole repository rewrote the repository's own files by text on every upgrade;
only the files taken from upstream ever need the names changed.

**How to apply.**

1. Take `scripts/check-cross-repo-sync.sh` from upstream by hand this once (it carries no names); step 3 writes the
   other shared files, `docs/upgrading-protocol.md` included.
2. Add the `sync-upstream` target to your `Makefile`, as upstream's: `@WRITE=1 scripts/check-cross-repo-sync.sh`.
3. From now on follow with `make sync-upstream` instead of copying trees and rerunning `make rename`. Your
   `scripts/rename.sh` needs no change.

## 2026-11-18 · 1

**What changed.** A repository may vendor `.proto` files Keel does not ship into directories of its own under
`third_party/`. `scripts/sync-proto.sh` now replaces only the four directories it owns (`keel`, `protovalidate`,
`googleapis`, `kratos`) and leaves any other directory there alone, and `buf.yaml` is no longer a shared file: each
repository adds a module per directory of its own.

**Why.** A contract importing, say, `google/type/money.proto` had nowhere to put it: `make proto` deleted all of
`third_party/`, and `buf.yaml`, where the new directory must be declared, had to match upstream verbatim.

**How to apply.**

1. Take `scripts/sync-proto.sh` and the shared docs from upstream (`make check-sync` lists them).
2. Delete the `keel-layout buf.yaml` line from `scripts/cross-repo-sync.list`. Keep `buf.yaml` as it is; from now on
   it is yours, and later upstream changes to it arrive as ledger entries.
3. For each extra `.proto` your contract imports, put it at a fixed version under `third_party/<name>/` and add a module
   for that directory to `buf.yaml`, with the same lint and breaking ignores as the others.
