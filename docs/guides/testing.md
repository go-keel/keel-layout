# Test seams and testing by layer

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before writing tests for the biz, service or data layer, or an end-to-end test.

## Three layers

Tests come in three layers (terms in `GLOSSARY.md`), each run by one command:

| Layer | What it tests | Servers | Command |
|---|---|---|---|
| unit test | one package, connected to no server | none; a real HTTP server from `httptest` and hand-written fakes of this repository's own interfaces are allowed | `make test` |
| narrow integration test | one package's boundary with a real server (build tag `integration`) | the real servers the `TEST_*` variables point at | `make integration` |
| end-to-end test | whole features, through the external interfaces of the service run as processes (`e2e/`, build tag `e2e`) | the same | `make e2e` |

A change is done when `make check test` passes (`make check` runs static checks only, no test). After changing code
that talks to a server, also run `make integration`; after changing external behaviour, also run `make e2e`.

**No emulators.** Databases, Redis and NATS are always real servers: no SQLite, miniredis or embedded NATS server.
Only this repository's own interfaces are replaced (hand-written fakes), plus `httptest`. An emulator differs from the
real server in details (SQLite once hid a MySQL reserved word and MySQL's `||`), and one way of doing each thing is
enough.

**Built like the binaries.** The three test commands build with `CGO_ENABLED=0`, as the production build does. The
race detector (`-race`) is on only on macOS, where it needs no cgo: Keel and the templates are checked for races
there, and tests run on Linux or Windows have no race detection.

**Test servers.** `make integration` and `make e2e` need four real servers, started however you like, and their
addresses in `TEST_MYSQL_DSN`, `TEST_POSTGRES_DSN`, `TEST_REDIS_ADDR` and `TEST_NATS_URL`; a missing one fails. The
servers must be for tests only: tests create and drop databases, empty Redis database 0 (the only one they use, as in
production and on a Redis Cluster) and delete every NATS stream. Two runs (`make integration` and `make e2e`, or two
of either) must not use the same servers at once; nothing locks them.

The code below uses the fictional `Article` domain (the same as in `docs/guides/api-endpoint.md`, `docs/guides/mq.md`
and `docs/guides/task.md`). The repository need not contain it; replace it with your own domain.

## Steps

1. Pick the seam by the layer under test. Tests sit next to the code, in the same package (`*_test.go`).

| Layer | Seam | Infrastructure |
|---|---|---|
| biz | hand-written fakes of biz interfaces (repo, port, `knats.Publisher`, `kasynq.Enqueuer`) | none |
| service | real UseCase on a fake repo (service and biz tested together) | none |
| data | the storage boundary: a fresh database per dialect, built from `migrations/*/<dialect>/0_schema.sql` | MySQL and PostgreSQL, `make integration` |
| server / `di.go` | no unit tests; `make build` compiles them | none |

2. Write the fake in the `_test.go` file of the layer under test: a small struct implementing the biz interface, with
   the compile-time assertion `var _ biz.XxxRepo = (*fakeXxxRepo)(nil)`, so an interface change fails the test build
   first.
3. Table-driven cases: `cases := []struct{ name string; ... }` with `t.Run`.
4. **`make test` passes with no database, Redis or NATS server and no environment variable.** Tests that need a real
   server are behind the build tag `integration` (see "data tests").
5. Assert behaviour the business can see (return values, domain errors, calls the fake recorded), never log output or
   counts of internal calls. Done when `make check test` passes.

## Code

### Fakes: hand-written implementations of biz interfaces

```go
// internal/biz/<domain>_test.go (same package as the code under test)
package biz

import (
    "context"

    "github.com/go-keel/keel/core/kasynq"
    "github.com/go-keel/keel/core/knats"
)

// fakeArticleRepo is a hand-written fake backed by a map, with only the behaviour the tested paths need.
type fakeArticleRepo struct {
    articles map[int64]*Article
    nextID   int64
}

var _ ArticleRepo = (*fakeArticleRepo)(nil)

func newFakeArticleRepo() *fakeArticleRepo {
    return &fakeArticleRepo{articles: map[int64]*Article{}, nextID: 1}
}

func (f *fakeArticleRepo) Create(_ context.Context, article *Article) (*Article, error) {
    created := *article
    created.ID = f.nextID
    f.nextID++
    f.articles[created.ID] = &created
    return &created, nil
}

func (f *fakeArticleRepo) FindByID(_ context.Context, id int64) (*Article, error) {
    article, ok := f.articles[id]
    if !ok {
        return nil, ErrArticleNotFound
    }
    return article, nil
}

// List / Update / Delete likewise, omitted.

// fakePublisher records calls for assertions; failWith injects a failure. It implements knats.Publisher, which
// EventType.Publish takes, so business tests need no NATS.
type fakePublisher struct {
    published []publishedEvent
    failWith  error
}

type publishedEvent struct {
    stream  knats.Stream
    subject string
}

var _ knats.Publisher = (*fakePublisher)(nil)

func (f *fakePublisher) Publish(_ context.Context, stream knats.Stream, subject string, _ any) error {
    if f.failWith != nil {
        return f.failWith
    }
    f.published = append(f.published, publishedEvent{stream, subject})
    return nil
}

func (f *fakePublisher) PublishEx(_ context.Context, stream knats.Stream, subject string, _ knats.Publishing) error {
    return f.Publish(context.Background(), stream, subject, nil)
}

// fakeEnqueuer records enqueued tasks. It implements only the byte-level Enqueue of kasynq.Enqueuer; the typed
// TaskType.Enqueue goes through it, so the test sees the task name. failWith set to kasynq.ErrTaskIDConflict tests
// the "conflict is an idempotent success" branch.
type fakeEnqueuer struct {
    enqueued []string // type names
    failWith error
}

var _ kasynq.Enqueuer = (*fakeEnqueuer)(nil)

func (f *fakeEnqueuer) Enqueue(_ context.Context, typeName string, _ []byte, _ ...kasynq.EnqueueOption) (*kasynq.TaskInfo, error) {
    if f.failWith != nil {
        return nil, f.failWith
    }
    f.enqueued = append(f.enqueued, typeName)
    return &kasynq.TaskInfo{}, nil
}
```

**Have the fake store the arguments it receives.** Whether what an upper layer computed (query conditions, ranges,
the fields to write) reaches the next layer unchanged can only be asserted that way: a repo fake's `List` records the
`List<Resource>Options` it got, and the test asserts the UseCase did not rewrite or drop it.

### biz tests: table-driven, asserting domain errors

```go
func TestArticleUseCase_CreateArticle(t *testing.T) {
    cases := []struct {
        name    string
        article *Article
        wantErr error
    }{
        {name: "ok", article: &Article{Title: "write tests"}},
        {name: "empty title", article: &Article{}, wantErr: ErrInvalidArgument},
        {name: "nil article", article: nil, wantErr: ErrInvalidArgument},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            uc := NewArticleUseCase(newFakeArticleRepo(), &fakePublisher{})

            _, err := uc.CreateArticle(t.Context(), tc.article)
            if !errors.Is(err, tc.wantErr) {
                t.Fatalf("CreateArticle() error = %v, want %v", err, tc.wantErr)
            }
        })
    }
}

func TestArticleUseCase_CreateArticle_PublishesEvent(t *testing.T) {
    publisher := &fakePublisher{}
    uc := NewArticleUseCase(newFakeArticleRepo(), publisher)

    if _, err := uc.CreateArticle(t.Context(), &Article{Title: "write tests"}); err != nil {
        t.Fatalf("CreateArticle: %v", err)
    }
    want := publishedEvent{StreamArticle, "published"}
    if len(publisher.published) != 1 || publisher.published[0] != want {
        t.Fatalf("expected %v published, got %v", want, publisher.published)
    }
}
```

`TaskNotifySubscribers` is a `kasynq.TaskType[ArticlePublishedEvent]` (a generic struct), not a string, so assert
the task name as the literal stored in `fakeEnqueuer.enqueued`:

```go
func TestNotifyUseCase_EnqueueNotify(t *testing.T) {
    client := &fakeEnqueuer{}
    uc := NewNotifyUseCase(client)

    err := uc.EnqueueNotify(t.Context(), &ArticlePublishedEvent{ID: 1, Title: "x"})
    if err != nil {
        t.Fatalf("EnqueueNotify: %v", err)
    }
    if len(client.enqueued) != 1 || client.enqueued[0] != "article:notify_subscribers" {
        t.Fatalf("expected article:notify_subscribers enqueued, got %v", client.enqueued)
    }
}

func TestNotifyUseCase_EnqueueNotify_IDConflictIsSuccess(t *testing.T) {
    client := &fakeEnqueuer{failWith: kasynq.ErrTaskIDConflict}
    uc := NewNotifyUseCase(client)

    // A redelivered event meets the unfinished task with the same ID: the earlier enqueue succeeded.
    if err := uc.EnqueueNotify(t.Context(), &ArticlePublishedEvent{ID: 1}); err != nil {
        t.Fatalf("ID conflict should be idempotent success, got %v", err)
    }
    if len(client.enqueued) != 0 {
        t.Fatalf("conflicting enqueue must not be recorded, got %v", client.enqueued)
    }
}
```

Assert domain errors with `errors.Is` against the sentinel (`biz.ErrXxx`), never by comparing error strings.

### service tests: real UseCase, fake repo

The service layer is thin (DTO↔DO conversion and boundary checks), so mocking the UseCase buys little: use the real
UseCase on a fake repo and test service and biz as one unit from the proto boundary (`<module>` is the module path in
this repository's `go.mod`):

```go
// internal/service/<resource>_test.go (the service package has its own fakeArticleRepo and noopPublisher)
package service

import (
    "context"
    "testing"

    "github.com/go-keel/keel/core/knats"

    v1 "<module>/api/<domain>/v1"
    "<module>/internal/biz"
)

type noopPublisher struct{}

var _ knats.Publisher = (*noopPublisher)(nil)

func (noopPublisher) Publish(context.Context, knats.Stream, string, any) error                { return nil }
func (noopPublisher) PublishEx(context.Context, knats.Stream, string, knats.Publishing) error {
    return nil
}

func newTestService() *ArticleService {
    uc := biz.NewArticleUseCase(newFakeArticleRepo(), noopPublisher{})
    return NewArticleService(uc)
}

func TestArticleService_CreateAndGet(t *testing.T) {
    svc := newTestService()
    ctx := t.Context()

    created, err := svc.CreateArticle(ctx, &v1.CreateArticleRequest{Title: "hello", Content: "world"})
    if err != nil {
        t.Fatalf("CreateArticle: %v", err)
    }
    if created.GetId() == 0 || created.GetTitle() != "hello" {
        t.Fatalf("unexpected reply: %+v", created)
    }

    got, err := svc.GetArticle(ctx, &v1.GetArticleRequest{Id: created.GetId()})
    if err != nil {
        t.Fatalf("GetArticle: %v", err)
    }
    if got.GetContent() != "world" {
        t.Fatalf("unexpected content: %q", got.GetContent())
    }
}
```

Assert at this layer: request conversion (FieldMask, list filter and sort fields), `offset`/`limit` passed through
unchanged, and rejection paths (before the sentinel the error is still a biz error, so `errors.Is` works). Middleware
(auth, validate, sentinel, and any this service adds) is not tested here: it is chain configuration, not service
logic.

### data tests: the storage boundary

A repo implementation is about SQL and mapping, which fakes cannot test, so data tests run on the real database. They
are `*_integration_test.go` files with `//go:build integration`; `make test` runs no data-layer tests,
`make integration` runs them.

```go
//go:build integration

// internal/data/<resource>_integration_test.go
func TestArticleRepo(t *testing.T) {
    testdb.ForEachDB(t, nil, testArticleRepo)
}

func testArticleRepo(t *testing.T, db *gorm.DB) {
    repo := NewArticleRepo(&kdb.DB{DB: db})
    // Create, read back, list with filters, partial update, delete, then assert the domain errors.
}
```

- `testdb.ForEachDB` is in `internal/testdb` (build tags `integration || e2e`, shared by narrow integration and
  end-to-end tests). It runs the test once per dialect the repository runs on (the `<dialect>` directories every
  `migrations/<database>/` has), as a subtest named after the dialect. Each run creates a fresh database on that
  dialect's server, applies `migrations/<database>/<dialect>/0_schema.sql` of the databases named in the second
  argument (every database when it is `nil`) and drops the database afterwards. The schema under test is therefore the
  one a new database gets; there is no separate test DDL. Incremental scripts are not run by tests: run one by hand
  on a database built from `1_baseline.sql` and the scripts before it, and check the result matches `0_schema.sql`.
- The servers come from `TEST_MYSQL_DSN` (`root:pass@tcp(127.0.0.1:3306)/?parseTime=true`) and `TEST_POSTGRES_DSN`
  (a URL, `postgres://postgres:pass@127.0.0.1:5432/postgres?sslmode=disable`); the account must be allowed to create
  databases. A missing variable for a dialect the repository runs on **fails** the test, because a skipped test reports a pass that
  tested nothing.
- Code that behaves differently per dialect (locking, case sensitivity, error details) gets a case that asserts the
  behaviour on both: that difference is what the second dialect is there to catch.
- Run `make integration` after changing data-layer code or a migration.

`make check` and `make test` never need a server; `make integration` and `make e2e` always need real ones.

### End-to-end tests

End-to-end tests run the binaries that ship, not the assembly code: they build each `cmd/<name>`, start it as a
process against the test servers and test it from outside, over HTTP, gRPC, NATS and Redis. Everything is under
`e2e/` with the build tag `e2e`; `make e2e` runs it (`go test -tags e2e ./e2e/...`), and `make e2e ARGS='-run /postgres'`
narrows the run.

**The harness** (`e2e/harness/harness.go`, a shared file: change it upstream) does the process work:

- `harness.Build(t, "api", ...)` builds `./cmd/<name>` once per test binary with `go build` and `CGO_ENABLED=0`, as a
  release is built; when the release build uses build tags, set `harness.BuildTags` to them before the first `Build`.
  On Windows it adds the `.exe` suffix.
- `harness.Workdir(t)` makes a temporary working directory holding only the committed `configs/config.yaml` and
  `configs/app.yaml`, so a developer's `*.dev.yaml` never reaches a test. A test may edit the copies before starting.
- `harness.Start(t, name, dir, env...)` starts the binary there and stops it when the test ends. The process gets
  `KEEL_` variables that fill placeholders in `config.yaml`: `harness.ServerEnv` maps `TEST_REDIS_ADDR` and
  `TEST_NATS_URL` to `${REDIS_ADDR}` and `${NATS_URL}`, sets `${LOGGER_FORMAT}` to `json` and points
  `${OTEL_ENDPOINT}` at a sink that accepts and drops telemetry (tracing stays on, so logs carry trace IDs, and no
  shutdown waits on an absent collector); `harness.DBEnv("default", driver, dsn)` fills `${DB_DEFAULT_DRIVER}` and
  `${DB_DEFAULT_SOURCE}`; the test adds the listen address (`KEEL_HTTP_ADDR`, `KEEL_GRPC_ADDR`, from
  `harness.FreeAddr`). **Keep these placeholders in `config.yaml`**; each has the local value as its default, so
  running the service by hand is unchanged.
- `harness.Config(t, "config.yaml", "app.domain")` reads a value from the committed config: assert against it instead
  of spelling out the service's own names (application name, cookie name, brand), which a fork replaces.
- Readiness: `WaitHTTP` (an HTTP server's `/health`), `WaitDial` (a gRPC port) or `WaitLog` on a startup line.
- `p.Stop(t)` stops as a deployment does: SIGTERM on macOS and Linux, and the test fails unless the process exits
  with 0 in time. Windows has no SIGTERM to send; there the process is killed and nothing is asserted (a clean stop is
  the framework's behaviour, covered on macOS).

**Rounds.** `main_test.go` in `e2e/` holds the table `rounds`: one row per deployment to test, naming the driver of each
database (the template: everything on MySQL, everything on PostgreSQL). Edit it to match how you deploy: one
database only, a different driver per database. Each round is one subtest and starts from empty servers:
`testdb.Create` builds a fresh database `e2e_<round>_<nanoseconds>` per database from
`migrations/<database>/<dialect>/0_schema.sql` (the service never creates tables) and drops it afterwards; Redis
database 0 is flushed and every NATS stream deleted at the start of the round, and left as they are at the end for
investigation. Rounds run one after another, and so do the cases in a round, in the order of the list `cases`.

**Cases.** A case is `func(t *testing.T, e *env)`, listed in `cases`, against the round's shared processes:

- Make your own data with a unique name (`uniq`), and assert only on it, or against the database (`e.db`) at the
  time: other cases add rows too.
- A case that needs a different config starts a process of its own: edit a `Workdir` copy, or pass extra `KEEL_`
  variables. Do not change the shared processes' config, except `app.yaml` when testing a reload.
- Background work is asserted on the JSON log: `p.WaitLog(t, msg, fields)` waits up to 10 seconds for a line with
  that `msg` and those fields, and on timeout fails with the process's last 50 lines; `p.Mark()` with `WaitLogSince`
  and `LogsSince` look only at what follows a point. State in between comes from the component's own admin API (the
  NATS client, asynq's `Inspector`), and what the service has no endpoint for (locks, rate limits, IDs) from calling
  the Keel package directly.
- No fixed sleep: poll for the condition with a deadline (`waitFor`). The exception is waiting on a server's clock,
  such as a rate-limit window.
- A case that stops or breaks a shared process restores it, or goes last.
- One comment line per case names its basis: the section of the usage docs whose behaviour it checks.

```go
// Basis: docs/<page>.md, "<section>".
func testArticlePublishedNotifies(t *testing.T, e *env) {
    id := e.createArticle(t, uniq("published"))
    e.worker.WaitLog(t, "subscribers notified", map[string]any{"article_id": id})
}
```

Run `make e2e` after changing external behaviour: an endpoint, an event or task flow, a config key, startup.

## Anti-patterns

```go
// ❌ unit tests connect to a real DB/Redis → red wherever there is no infrastructure
// ✅ fakes by default; real servers behind the integration build tag (make integration)

// ❌ an emulator (SQLite, miniredis, an embedded NATS server) standing in for a real server
// ✅ the real server the TEST_* variable points at

// ❌ a mock framework or code generation for fakes
// ✅ hand-written fakes: small interfaces, explicit behaviour, a var _ compile-time assertion

// ❌ a fake missing a method, with the compile error reported outside the test file
// ✅ var _ biz.ArticleRepo = (*fakeArticleRepo)(nil) asserted on the spot

// ❌ comparing error strings
if err.Error() != "article not found" { ... }
// ✅ errors.Is(err, biz.ErrArticleNotFound)

// ❌ starting a full HTTP server to test service logic (slow, brittle, tests the framework)
// ✅ call the service method; HTTP behaviour is the proto contract's and the framework's job

// ❌ asserting log output or internal call counts
// ✅ asserting return values, domain errors and the business effects the fake recorded
```

## Related

- `docs/guides/domain-model.md`: repo and port interfaces, compile-time assertions
- `docs/guides/errors.md`: domain errors and sentinels
- `docs/guides/assembly.md`: `di.go` (tests skip it and construct directly)
