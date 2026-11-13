# Keel Layout

A Keel microservice scaffold (fork it, rename, and start). One project shares its business logic, data layer and
contract definitions, and builds applications for different purposes from different `cmd` entry points.

It ships with a **Todo example resource that runs through all five servers**:

```
CreateTodo (HTTP/gRPC) ──► biz.CreateTodo ──┬─► store with gorm            (data)
                                            └─► publish todo_created event (NATS)
worker: consumer handles todo_created ──► enqueue todo:send_reminder (asynq)
        task worker handles the reminder task
        scheduler enqueues todo:daily_report daily at 08:00 (cron)
```

The same `internal/biz` code serves HTTP through `cmd/api`, gRPC through `cmd/svc`, and consumers through
`cmd/worker`.

## Project structure

The three `cmd/` entry points share one `internal/{biz,data,service}`: `cmd/api` is the public HTTP API (with raw
route examples such as SSE and a webhook), `cmd/svc` is an internal gRPC microservice, and `cmd/worker` runs event
consumers, the task worker and the cron scheduler. The full directory layout is in `docs/practices/structure.md`.

## Run it (this repo's example)

```bash
make init      # installs only buf (pinned; the generator plugins are pinned in buf.gen.yaml, no install needed)
make all       # proto + config + generate
make build     # builds bin/api bin/svc bin/worker
```

Tests run without any infrastructure (the biz/service layers use a fake repo):

```bash
go test ./...
```

**To actually start a process, first check what it needs**; the three cmds do not depend on the same set:

| Process | MySQL | NATS | Redis |
|---|:---:|:---:|:---:|
| `bin/api` (HTTP `:8080`) | ✓ | ✓ | |
| `bin/svc` (gRPC `:9000`) | ✓ | ✓ | |
| `bin/worker` (event consumers + async tasks + cron) | | ✓ | ✓ |

```bash
# 1. Local override: config.dev.yaml is ignored by .gitignore; create it from the example and adjust the connection
#    string to your machine (keep both time-zone parameters)
cp configs/config.dev.example.yaml configs/config.dev.yaml
#    Without a running OTEL collector, also add `otel: {enabled: false}` to it

# 2. Create tables: no AutoMigrate; a new database runs the complete schema by hand
mysql -uroot -p123456 keel < migrations/business/0_schema.sql

# 3. Start a process
./bin/api
```

Config precedence: `configs/config.yaml` (the real main config, the one production deploys)
← `configs/config.dev.yaml` (development override only) ← environment variables prefixed `KEEL_` (highest).

Choosing between a contract endpoint and a raw route: `docs/guides/raw-http.md`.

Try it:

```bash
# Create (triggers the todo_created event → the worker enqueues a reminder task)
curl -X POST localhost:8080/v1/todos -H 'Content-Type: application/json' -d '{"title":"hello keel"}'
# List (server-side paging offset/limit + total; completed is tri-state 0 all/1 yes/2 no; sort is an enum)
curl 'localhost:8080/v1/todos?offset=0&limit=10&completed=2&sort=SORT_UPDATE_TIME_DESC'
# Contract streaming SSE (runs the middleware chain, protojson frames)
curl -N localhost:8080/v1/todos/watch
# Contract CSV export (HttpBody, runs the chain)
curl localhost:8080/v1/todos/export
# Raw SSE snapshot stream (bypasses the chain)
curl -N localhost:8080/examples/sse
# Raw download (Range/resumable)
curl -O localhost:8080/examples/download
# Webhook (HMAC-SHA256 signature). webhook_secret is empty by default → 503; first put
#   custom: {webhook_secret: local-dev-secret}
# in configs/app.dev.yaml (git-ignored dev overlay; restart the process so it is picked up).
SECRET=local-dev-secret
BODY='{"event":"ping"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')  # $NF: LibreSSL (macOS) prints only the hex
curl -X POST localhost:8080/examples/webhook -H "X-Signature: $SIG" -d "$BODY"
```

## Fork into your own repo

**Rename before generating anything.** `make all` produces `*.pb.go` files carrying
`github.com/go-keel/keel-layout`, and the rename script **does not touch generated artifacts** (sed on `.pb.go`
corrupts rawDesc and only blows up at runtime); in the wrong order you have to regenerate again.

```bash
make init                             # install buf
make rename MODULE=example.com/demo   # replaces the module path and the proto package under api/ in one go; idempotent
make init-hooks                       # install the commit and merge hooks in .githooks/ (block changes to the propagation surface without an UPGRADING.md entry, check dead links)
make all                              # only now does generation produce your own module path
```

The proto package defaults to the last segment of the module path: `example.com/demo` gives `package api.demo.v1`,
and the directory under `api/` becomes demo; `-`, `.` and the like in that segment become `_` (`example.com/shop-api`
gives `api.shop_api.v1`), and when the last segment is a version suffix like `/v2` the one before it is used.
It goes into full gRPC method names and OpenAPI type names and is the service's public name; to choose another, add
`API_DOMAIN=xxx`.

**Switch from `lint-strict`, which this repo uses on itself, to the derived-repo tier `lint`**, in two places: the
`check` target in `Makefile` and the `make lint-strict` command in `.githooks/pre-commit`. If your repo will itself be
forked, keep `lint-strict` instead and change `keel-layout` in its patterns to your repository's directory name.
`lint-strict` checks everything and reports the paths of your own business area; `lint` checks only what upstream is
responsible for (see `docs/adr/layout-0005-doc-link-check-distribution-boundary.md`). Also delete the
`scripts/check-english.sh` line in the `check` target: requiring English is this template's own rule, and your
service writes in its team's language. Do this before deleting the examples, or the final `make check` below fails.

**A repo nobody will fork** writes no ledger entries of its own: delete the ledger check (the first `if` block) in
`.githooks/pre-commit`, or nearly every commit is stopped. Its `UPGRADING.md` then holds only "following upstream"
entries.

**Enable the cross-repo comparison list.** Change the leading `#keel-layout` of the lines in the last block of
`scripts/cross-repo-sync.list` to `keel-layout`, or `check-sync` in `make check` fails.

**Replace the template identity in config with your own.** `make rename` changes only Go, proto and docs, not YAML:
`app.name`, `app.domain` and the database name in `database.default.source` in `configs/config.yaml`, the database name
in `configs/config.dev.example.yaml` still carry the template's identity (`app.name` is keel-layout; `app.domain`, your
service's short business name, and the database name are `keel`). They go into logs, traces and database connections;
left unchanged, your service still calls itself keel-layout at runtime; miss the dev example, and the local override
copied per "Run it" changes the database name back.

**Rewrite the entry files outside the shared segments.** The paragraphs that `shared segment begin` /
`shared segment end` delimit in `AGENTS.md` and `GLOSSARY.md` are upstream's conventions: keep them unchanged. The
sections outside them ("This repository: keel-layout" in `AGENTS.md`, "This repo: keel-layout" in `GLOSSARY.md`)
describe the template itself; replace them with your service (module
name, entry points, the commands and rules specific to your repo). Put practices specific to your repo in documents
of your own and register them in the routing table outside the segments; do not change the shared files under
`docs/guides/` and `docs/practices/`.

### Delete the examples

The template carries a Todo example resource that runs through all five servers; delete it completely after
forking. The boundary of the example is marked with comments, and only where the compiler cannot help:

- **"Example file: delete the whole file after forking"** at the top of a file: delete the file (step 1).
- **"Example: delete after forking"** at the end of a line: delete that line.
- **"Example: delete after forking"** on a line of its own: delete it and the comment lines and the one declaration,
  field or config key directly below it.
- **"Example: delete after forking (through "Example end")"**: delete everything down to and including the matching
  "Example end" line.

After every deletion, `go build ./...` reports imports that are now unused; delete them too. Three steps, in this
order:

1. **Delete whole example files.** Delete every file whose header says "Example file: delete the whole file after
   forking", then regenerate (`make proto` first clears the generated artifacts under `api/`, so a deleted proto
   leaves no orphans):

   ```bash
   grep -rl "Example file: delete the whole file after forking" api internal migrations --exclude='*.pb.go' | xargs rm
   make proto
   ```

   `internal/data/testdb_integration_test.go` stays: it builds the test database from `migrations/*/0_schema.sql`
   for your own data-layer tests. `internal/data/model.go` stays as the home of your GORM models; delete its marked
   block now, because it refers to the deleted `biz.Todo`. `migrations/business/0_schema.sql` stays too: step 3
   deletes the example table from it. With the example's data-layer tests gone, `make integration` runs no test until
   you add your own.

2. **Fix the interwoven places by compile errors.** `go build ./...` points at the broken references in
   `internal/server/`, `internal/service/` and `cmd/*`; delete them one by one. It reports one batch per run, and
   for a package with many errors stops after the first ten ("too many errors"), so delete and run again until it
   compiles; this usually takes several rounds. In this step the compiler is the marker; when an error sits inside a
   marked block, delete the whole block.

   When the compiler reports `db`, `natsClient` and the like as "declared and not used", do not keep them as `_`;
   delete them together with their construction and cleanup: `di.go` assembles only what this cmd uses, and a kept
   database, NATS or Redis connection still connects at startup and fails without the corresponding service. Four
   things compile but fail at startup, so check them yourself:

   - a publisher with no stream left: `knats.NewPublisher(natsClient)` returns `ErrNoStreams`; delete the publisher;
   - a consumer server with no subscriptions: `knats.NewConsumerServer` returns `ErrNoConsumers`; stop building it
     (and the NATS client, once nothing else uses it) in `cmd/worker/di.go` and drop its parameter from `newApp` in
     `cmd/worker/main.go`;
   - a task worker or scheduler with nothing registered still needs Redis; with no task and no cron job left, stop
     building them, the task client and Redis in `cmd/worker/di.go`, and drop their parameters in `main.go`;
   - a cmd with no server left: once the consumer server, task worker and scheduler are all gone, the worker fails
     startup with `app.no-servers`. Keep `cmd/worker` as the place for your first consumer or task, and leave it out
     of deployment until then.

   Keep the registration functions in `internal/server/` (`grpc.go`, `http.go`, `consumer.go`, `task.go`,
   `scheduler.go`) and `service.TaskHandler`, now empty, as the places for your first endpoints, consumer, task and
   cron job. With the webhook gone, `cmd/api` reads no field of `app.yaml`: rename its `wireApp` parameter to `_` and
   copy the comment above `wireApp` in `cmd/svc/di.go`. With no database left, also delete the blank import of the
   database driver (`core/kdb/mysql`) in `cmd/api/main.go` and `cmd/svc/main.go`.
   Wire everything back per `docs/guides/assembly.md` when you add your first resource.

3. **Delete the leftovers the compiler cannot see.** The chain of error code, sentinel error, translation function,
   i18n message and config field still references itself after the example is gone and compiles fine. Delete each
   place marked "Example: delete after forking", by the scope rules above:

   ```bash
   grep -rn "Example: delete after forking" api internal configs cmd scripts migrations --exclude='*.pb.go'
   ```

   Once there are no hits, regenerate with `make proto && make config`. The i18n message files carry no markers (goi18n
   rewrites files and drops comments); after deleting the translation function run
   `make i18n-extract && make i18n-merge`, and the example entries in each language's `active.*.toml` disappear with the
   code. An empty `custom:` in `configs/app.yaml` and an empty `message Custom` in `internal/conf/app.proto` are
   fine: they are where your first service config field goes. Run `go mod tidy` to drop the dependencies only the
   example used. Then delete the "Example code" section outside the segments of `AGENTS.md` (convention documents do not
   reference example code, and once the examples are gone that is the only place pointing at them). In this README,
   rewrite the introduction, "Project structure" and "Run it" for your service; "Delete the examples" can go, while
   "Fork into your own repo" stays: `make rename` points at it. Finally run `make check`.

Both greps exclude `*.pb.go`: the marker comments are carried into generated artifacts, and the hits there vanish
on regeneration; do not edit generated artifacts by hand. The raw route examples all sit under the `/examples/`
prefix and go away with the first two steps.

**Start the ledger, then commit.** Delete every entry in `UPGRADING.md`, keeping its introduction: they are upstream's
history, already in your fork. Add the first "following upstream" entry, your sync point (`docs/upgrading-protocol.md`):

```markdown
## <today> · 1

> Follows keel-layout <short hash of the keel-layout commit you forked>

Forked with `make rename MODULE=<your module>` (plus any `API_DOMAIN=` you passed); later overwrites rerun it the same way.
```

Then commit everything with `git add -A && git commit`: the ledger is in the commit, so the hook lets it through.

From here on work per `AGENTS.md` and `docs/guides/`; to follow template changes read `UPGRADING.md` (how entries
are written and how pending ones are counted: `docs/upgrading-protocol.md`).

## Application types

- **API (cmd/api)**: public HTTP, JWT off by default (can be enabled in config), i18n and the error sentinel on
- **Service (cmd/svc)**: internal gRPC, sharing the same biz/data
- **Worker (cmd/worker)**: message queue consumers (event broadcast) + asynq tasks (point-to-point) + cron scheduling

## Development conventions

- Standing contract and task routing: `AGENTS.md` (shared by AI agents and developers)
- Guides: `docs/guides/` (read per task as needed; like the practices, shared verbatim along the whole chain)
- Practices: `docs/practices/` (task-independent, shared verbatim along the whole chain; read before writing Go
  code, creating tables, opening transactions or writing text users read)
- Assembly: hand-written di.go, constructors return `(T, cleanup, error)`, no DI tool
- Logging: standard library slog (`slog.InfoContext`), automatically correlated with traces
