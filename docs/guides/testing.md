# Test seams and testing by layer

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before writing tests for the biz, service or data layer.

The code below uses the fictional `Article` domain (the same as in `docs/guides/api-endpoint.md`, `docs/guides/mq.md`
and `docs/guides/task.md`). The repository need not contain it; replace it with your own domain.

## Steps

1. Pick the seam by the layer under test. Tests sit next to the code, in the same package (`*_test.go`).

| Layer | Seam | Infrastructure |
|---|---|---|
| biz | hand-written fakes of biz interfaces (repo, port, `knats.Publisher`, `kasynq.Enqueuer`) | none |
| service | real UseCase on a fake repo (service and biz tested together) | none |
| data | the storage boundary: a fresh MySQL database built from `migrations/*/0_schema.sql` | MySQL, `make integration` only |
| server / `di.go` | no unit tests; `make build` compiles them | none |

2. Write the fake in the `_test.go` file of the layer under test: a small struct implementing the biz interface, with
   the compile-time assertion `var _ biz.XxxRepo = (*fakeXxxRepo)(nil)`, so an interface change fails the test build
   first.
3. Table-driven cases: `cases := []struct{ name string; ... }` with `t.Run`.
4. **`make check` passes with no database, Redis, NATS or Docker.** Tests that need real storage are behind the build
   tag `integration` (see "data tests").
5. Assert behaviour the business can see (return values, domain errors, calls the fake recorded), never log output or
   counts of internal calls. Done when `make check` passes.

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
`ListOptions` it got, and the test asserts the UseCase did not rewrite or drop it.

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
are `*_integration_test.go` files with `//go:build integration`; `make check` runs no data-layer tests,
`make integration` runs them.

```go
//go:build integration

// internal/data/<resource>_integration_test.go
func TestArticleRepo(t *testing.T) {
    repo := NewArticleRepo(openTestDB(t))
    // Create, read back, list with filters, partial update, delete, then assert the domain errors.
}
```

- `openTestDB` is in `internal/data/testdb_integration_test.go`: it creates a fresh database on the MySQL server in
  `TEST_MYSQL_DSN`, applies the complete schema of every database (`migrations/*/0_schema.sql`) and drops the database
  afterwards. The schema under test is therefore the one a new database gets; there is no separate test DDL.
  Incremental scripts are not run by tests: run one by hand on a database built from `1_baseline.sql` and the scripts
  before it, and check the result matches `0_schema.sql`.
- A missing `TEST_MYSQL_DSN` **fails** the test, because a skipped test reports a pass that tested nothing.
- Run `make integration` after changing data-layer code or a migration.

`make check` never needs a database or Docker; `make integration` always does.

## Anti-patterns

```go
// ❌ default tests connect to a real DB/Redis → red wherever there is no infrastructure
// ✅ fakes by default; real storage behind the integration build tag (make integration)

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
