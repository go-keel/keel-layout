# Domain model and data layer

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before creating or changing a domain object (DO), a GORM model (PO), a repo or port interface, or its data
layer implementation.

The code below uses a fictional `Article` domain that this repository may not have; use your own domain name.

## Steps

1. Put **everything** for the domain in `internal/biz/{domain}.go`: the DO, status enums, repo and port interfaces, the
   use case, input and output types. No `biz/entities/` subdirectory: an entity lives in the file of the use cases
   that use it, so nobody jumps between files.
2. Add new domain errors to `internal/biz/errors.go` (mapping in `docs/guides/errors.md`).
3. Define the private GORM model in `internal/data/model.go` with its `TableName()`, `toBiz()` and `new<Resource>()`
   (up to 5 models in that file; beyond that split per domain into `internal/data/{domain}_model.go`).
4. Implement the repo in `internal/data/{domain}.go`: a compile-time assertion, and a constructor returning the
   interface type.
5. Write the schema change into `migrations/<database>/0_schema.sql`, plus an incremental script once the database is
   in production (**no AutoMigrate**; in the same commit as the model change; see "Schema" below).
6. Wire the new constructors in every `cmd/<app>/di.go` that uses them, in the order data → biz → service.
7. Done when `make check` passes (the `var _` assertion catches an interface mismatch here).

When one `biz/{domain}.go` passes about 400 lines, split by verb: `biz/{domain}.go` (entities, interfaces, query use
cases), `biz/{domain}_ingest.go` (a complex write use case), and so on.

## Code patterns

### Biz layer: one file per domain

The DO is a pure business model with no proto and no storage tags; a status field is always a typed enum
(`type XxxStatus string` + named constants), never a bare string:

```go
// internal/biz/article.go (example)
type ArticleStatus string

const (
    ArticleStatusDraft     ArticleStatus = "draft"
    ArticleStatusPublished ArticleStatus = "published"
)

type Article struct {
    ID         int64
    Title      string
    Content    string
    Status     ArticleStatus
    CreateTime time.Time
    UpdateTime time.Time
}
```

Biz declares the repo interface and data implements it; this inverted dependency is what keeps the layers apart:

```go
type ArticleRepo interface {
    Create(ctx context.Context, article *Article) (*Article, error)
    FindByID(ctx context.Context, id int64) (*Article, error)
    List(ctx context.Context, opts ListOptions) ([]*Article, int64, error)
    // Update writes only the listed fields: a partial update must be one UPDATE; reading the row, merging in
    // memory and writing the whole row back overwrites the other half of a concurrent change.
    Update(ctx context.Context, article *Article, fields []string) (*Article, error)
    Delete(ctx context.Context, id int64) error
}
```

Repo methods follow persistence semantics: create and update are two methods (insert vs. update by field), not one
`Save`.

The use case holds the repo (and the ports, publisher and other dependencies it needs); its constructor only assigns
fields:

```go
type ArticleUseCase struct {
    repo      ArticleRepo
    publisher knats.Publisher // only when it publishes events; see the mq guide
}

func NewArticleUseCase(repo ArticleRepo, publisher knats.Publisher) *ArticleUseCase {
    return &ArticleUseCase{repo: repo, publisher: publisher}
}
```

A repo (local storage) and a port (an external system) are different interfaces: local storage is `<Resource>Repo`,
calls to another service or API are `<System>Port`. Where each is declared, implemented and wired is in
`docs/guides/grpc-client.md`; a port shared by several domains goes in `biz/ports.go`, one used by a single domain stays
in its file.

Define a Request struct when a method has more than 3 parameters or they come from several sources; define a Result
struct when the return has several independent fields; otherwise pass parameters and return the entity or error
directly. `ListOptions` for list queries (filters, sort, `Offset` / `Limit` in one struct, translated into a storage
query by data) is the common case of the first. Another example:

```go
// More than 3 parameters, or parameters from several sources: a Request struct,
// right before the use case method.
type IngestRequest struct {
    EventID  string
    ForceNew bool
    SourceID string
}

// A return with several independent fields (not one entity or error): a Result struct.
type IngestResult struct {
    ID      int64
    Created bool
}
```

| File | Holds |
|------|-------|
| `biz/{domain}.go` | entities, status enums, repo and port interfaces, use case, input and output types |
| `biz/ports.go` | external port interfaces shared **across domains** only |
| `biz/errors.go` | all domain errors |

### Data layer: private GORM model and toBiz

The private GORM models (POs) live in `internal/data/model.go`; up to 5 in that file, beyond that split per domain into
`internal/data/{domain}_model.go`. A PO's `TableName()`, `toBiz()` and `new<Resource>()` sit in the same file as the
PO.

**A PO is named after its table in lowerCamelCase, without a `Model` suffix**: table `article` → `article`, table
`sys_user` → `sysUser`. The lowercase start keeps it unexported, so the compiler keeps POs inside the data package. It
also means a table name cannot be a variable name in the data package; use `a`, `m`, `row` and the like.
`TableName()` is always explicit, never left to GORM's inference. Tags carry only what changes how GORM reads and
writes (`primaryKey`, `autoIncrement`, `column`, `default`, `autoCreateTime`, `autoUpdateTime`); lengths, `not null`,
types and indexes live only in `migrations/*.sql` (`docs/practices/database.md`), since GORM reads those tags only
for AutoMigrate, which is not used:

```go
// internal/data/model.go
type article struct {
    ID        int64 `gorm:"primaryKey;autoIncrement"`
    Title     string
    Content   string
    Status    string
    CreatedAt time.Time `gorm:"autoCreateTime"`
    UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (article) TableName() string { return "article" }
```

A model that is read implements `toBiz()`, a method on the PO (PO → DO); the write direction is the free function
`new<Resource>()` (DO → PO). A method rather than a free function tells several POs' conversions in one file apart. A
write-only model (an audit log, say) needs no `toBiz()`.

```go
// internal/data/model.go, right after the PO

// newArticle converts DO → PO (write direction).
func newArticle(a *biz.Article) *article {
    return &article{
        ID:      a.ID,
        Title:   a.Title,
        Content: a.Content,
        Status:  string(a.Status),
    }
}

// toBiz converts PO → DO (read direction).
func (m *article) toBiz() *biz.Article {
    return &biz.Article{
        ID:         m.ID,
        Title:      m.Title,
        Content:    m.Content,
        Status:     biz.ArticleStatus(m.Status),
        CreateTime: m.CreatedAt,
        UpdateTime: m.UpdatedAt,
    }
}
```

Money columns are always `DECIMAL`, never `float`; column type rules are in `docs/practices/database.md`, the Go side
in the anti-patterns below.

### Data layer: the repo implementation

Every repo implementation has a compile-time interface check, and its constructor returns the interface type:

```go
// internal/data/article.go (example)
type articleRepo struct {
    db *kdb.DB
}

var _ biz.ArticleRepo = (*articleRepo)(nil)

func NewArticleRepo(db *kdb.DB) biz.ArticleRepo {
    return &articleRepo{db: db}
}
```

Queries always pass `WithContext(ctx)` and map `gorm.ErrRecordNotFound` to a domain sentinel, so upper layers never
see the driver:

```go
func (r *articleRepo) FindByID(ctx context.Context, id int64) (*biz.Article, error) {
    var m article
    err := r.db.WithContext(ctx).First(&m, id).Error
    switch {
    case errors.Is(err, gorm.ErrRecordNotFound):
        return nil, biz.ErrArticleNotFound
    case err != nil:
        slog.ErrorContext(ctx, "failed to find article", "error", err, "id", id)
        return nil, errors.Join(biz.ErrUnexpectedError, err)
    }
    return m.toBiz(), nil
}
```

| Case | Return |
|------|--------|
| "Not found" is a normal business state | `return nil, nil`; the caller decides |
| The record must exist | the domain error `biz.ErrXxxNotFound` (as in `FindByID`) |
| A primary or unique key collision | `errors.Is(err, gorm.ErrDuplicatedKey)` → a domain error. Map it directly only when this insert can collide on that one key alone (`keel:docs/database.md#driver-errors`; the pattern is in `docs/guides/errors.md`) |

Whether "not found" is an error is business meaning: map it to a specific domain error in data when you can; leave it
to biz when it needs business context.

Before a repo method opens a transaction, takes a lock or does a conditional update, read
`docs/practices/transaction.md`.

### Schema: hand-run .sql scripts only

**No schema changes from code** (`AutoMigrate` or any programmatic DDL that changes a table). The one exception is
partition maintenance: for a table the creation script made partitioned, code may create new partitions and drop
expired ones on a schedule, without touching columns, indexes, the partitioning scheme or other tables (see
`docs/practices/database.md`). The schema is managed only by `.sql` scripts under `migrations/`, **one directory per
database**: `migrations/business/` by default; a repository with more databases adds `migrations/<database>/` for each,
named as it likes. A directory owned by an upstream template (listed outside the segments) is overwritten whole when
following upstream and never edited here.

Each directory holds three kinds of files, listed in this order:

```
migrations/business/
├── 0_schema.sql                          complete schema at its latest shape; a new database runs only this
├── 1_baseline.sql                        0_schema.sql as it was at the baseline
└── 20261103142000_add_article_tag.sql    one change on top of the baseline (YYYYMMDDHHMMSS_description.sql)
```

- **`0_schema.sql` is always complete and current.** Every change is edited into it, in the same commit as the GORM
  model change, so a new database is created from one readable file.
- **Before the first production deployment there is only `0_schema.sql`.** At that deployment, copy it to
  `1_baseline.sql`; from then on every change also adds an incremental script, and `1_baseline.sql` is not edited.
- **An existing database runs the incremental scripts it has not run yet**, in file-name order. Their names start
  with the moment they are created (`date +%Y%m%d%H%M%S`, never a placeholder like `000000`): branches need no
  coordination on numbers, and name order is run order.
- **The baseline may move forward**: replace `1_baseline.sql` with the current `0_schema.sql` and delete the
  incremental scripts it now contains. **Before doing it, ask the user to confirm that every environment has run
  those scripts**: an environment that has not can no longer catch up from the files.
- Every environment, local included, runs scripts by hand: `mysql ... < migrations/business/0_schema.sql`.
- **A template ships only `0_schema.sql`.** Following upstream overwrites the complete schema of a directory the
  template owns; the `ALTER` an existing database needs is in the matching ledger entry in `UPGRADING.md`.

Table design rules (keys, IDs, names, charset, time, indexes, soft delete) and how to write a script are in
`docs/practices/database.md`; this section only covers how scripts are organised.

### Event structs

An event payload struct is declared in biz with its event contract (`knats.NewEventType[P]`), in the domain file of
the use case that publishes it:

```go
// internal/biz/article.go (example)
type ArticlePublishedEvent struct {
    ID    int64  `json:"id"`
    Title string `json:"title"`
}
```

Streams, subjects, payload binding, consumers and cron registration are in `docs/guides/mq.md`.

### Redis cache in a repo

A teaching example, the same fictional domain as "Extra components" in `docs/guides/assembly.md`, showing reads, writes
and fallback when the cache fails (assume `Article` has a unique `Slug` field to look one up by).

The cache is an optional fast path inside data and does not change `biz.ArticleRepo`: biz does not know, and does not
need to know, whether `GetBySlug` hit the cache or the database.

```go
// for example in internal/data/article_cache.go (imports omitted)
type articleCache struct {
    rdb *kredis.Redis
}

func newArticleCache(rdb *kredis.Redis) *articleCache {
    return &articleCache{rdb: rdb}
}

func cacheKey(slug string) string { return "article:" + slug }

// get returns (nil, nil) on a miss: redis.Nil is not an error, it means "nothing cached".
func (c *articleCache) get(ctx context.Context, slug string) (*biz.Article, error) {
    data, err := c.rdb.Get(ctx, cacheKey(slug)).Bytes()
    if errors.Is(err, redis.Nil) {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    var a biz.Article
    if err := json.Unmarshal(data, &a); err != nil {
        return nil, err
    }
    return &a, nil
}

// set adds random jitter to the TTL: many keys expiring at once would send all their requests to the database.
func (c *articleCache) set(ctx context.Context, a *biz.Article) error {
    data, err := json.Marshal(a)
    if err != nil {
        return err
    }
    ttl := 10*time.Minute + time.Duration(rand.IntN(120))*time.Second
    return c.rdb.Set(ctx, cacheKey(a.Slug), data, ttl).Err()
}
```

The repo combines storage and cache: cache first, then the database on a miss, then write back. **A cache failure
never blocks the request**; it falls back to the database, because a fast path that is down must not take the
endpoint down with it.

```go
// for example in internal/data/article.go
func (r *articleRepo) GetBySlug(ctx context.Context, slug string) (*biz.Article, error) {
    if cached, err := r.cache.get(ctx, slug); err != nil {
        // Log and continue with the database: the endpoint stays available.
        slog.WarnContext(ctx, "cache get failed, falling back to db", "error", err, "slug", slug)
    } else if cached != nil {
        return cached, nil // a hit is a normal path; no log
    }

    a, err := r.getFromDB(ctx, slug) // the gorm query from the previous section
    if err != nil {
        return nil, err
    }
    if err := r.cache.set(ctx, a); err != nil {
        // The data is already in hand; a failed write-back must not fail the request.
        slog.WarnContext(ctx, "cache set failed", "error", err, "slug", slug)
    }
    return a, nil
}
```

In short: a miss is not an error and is not logged; a cache failure falls back to the database; every TTL has
jitter; **updating or deleting a record deletes its cache key**, or the next read returns stale data; do not rely on
the TTL to clean up.

### Distributed lock: one instance at a time

A teaching example in the same fictional domain. How the lock works and why it is not renewed:
`keel:docs/redis.md#distributed-lock`. Failing to get the lock returns `lock.ErrLockNotAcquired`, which is a normal
state (another instance is on it): **skip, do not fail**.

```go
// for example in internal/biz/article.go: ArchiveUseCase lets at most one instance archive at a time,
// provided one run finishes within the lock's TTL (60 seconds)
import "github.com/go-keel/keel/core/kredis/lock"

type ArchiveUseCase struct {
    repo   ArticleRepo
    locker lock.Locker
}

func (uc *ArchiveUseCase) ArchiveExpired(ctx context.Context) error {
    // Size the TTL for the worst case, generously: the lock is not renewed and another instance takes it at expiry.
    release, err := uc.locker.TryAcquireLock(ctx, "archive:article", 60)
    if errors.Is(err, lock.ErrLockNotAcquired) {
        slog.InfoContext(ctx, "archive already running elsewhere, skip")
        return nil // not an error: skip this run and wait for the next schedule
    }
    if err != nil {
        return errors.Join(ErrUnexpectedError, err)
    }
    // defer releases even on panic; it only returns the lock and cannot protect anything past the TTL
    defer release()

    return uc.repo.ArchiveExpired(ctx)
}
```

**Work under the lock must finish within the TTL.** When it may not, split it into batches that each fit in the TTL
and lock each batch, make the operation safe to repeat (a conditional update, a business idempotency key), or use a
database row lock for long transactions. Alert on the `ttl_exceeded=true` ERROR log in production. Assembly order (after
`kredis.NewRedis`) is in `docs/guides/assembly.md`.

## Where field constraints live

**Domain invariants (length, range, format, state transitions) belong in biz, not only in proto.** A `buf.validate`
rule is bound to one request message and guards only that entry point; a second way to write the same field (Update,
editing your own profile, an import, ...) bypasses it and the data goes straight to the database. The typical pair:
`CreateArticleRequest.title` has `max_len: 200`, but Update takes `UpdateArticleRequest{ Article article, FieldMask
update_mask }`, and `Article` has no rule, so an over-long title gets in. Every entry point therefore calls one
validation function in biz:

```go
// internal/biz/article.go (example)
// The limit counts characters, not bytes, with the same value as the proto max_len; it must exist here too:
// the proto rule only guards Create, and Update gets around it.
const maxTitleLen = 200

func validateTitle(title string) error {
    if title == "" || utf8.RuneCountInString(title) > maxTitleLen {
        return ErrInvalidArgument
    }
    return nil
}
```

Create and Update both call it (Update only when the FieldMask lists `title`).

**Count length in characters with `utf8.RuneCountInString`, not `len()`.** `len()` counts bytes and would reject 200
Chinese characters (600 bytes), while the proto `max_len` and the database's `VARCHAR(200)` both count characters: the
transport accepts it, the column fits it, and the domain rejects it. A mismatch shows up as data that gets in through
one entry point and not another.

The proto rule is not redundant: it stops obvious violations in the chain, with the field path in the message. The two
values change together; grep the constant name when you add a rule.

## Anti-patterns

```go
// ❌ proto types in biz
func (uc *ArticleUseCase) Get(ctx context.Context, req *pb.GetArticleRequest) (*pb.Article, error)
// ✅ biz speaks DO only
func (uc *ArticleUseCase) Get(ctx context.Context, id int64) (*Article, error)

// ❌ float for money (loses precision)
total := 10.50 + 3.30
// ✅ decimal
total, _ := decimal.NewFromString("10.50")
total = total.Add(decimal.NewFromInt(3))

// ❌ a bare string status
article.Status = "PUBLISHED"
// ✅ a typed enum constant
article.Status = ArticleStatusPublished

// ❌ mixing the Repo and Port suffixes
type UserRepo interface{}    // an external gRPC/HTTP API is not a Repo
type ArticlePort interface{} // the local database is not a Port

// ❌ an exported PO, a Model suffix, or a PO returned out of data
type ArticleModel struct{ ... }
func (r *articleRepo) Get(...) (*article, error)
// ✅ the PO is the table name in lowerCamelCase (type article struct); call toBiz() before it leaves data

// ❌ a constructor returning the concrete type
func NewArticleRepo(db *kdb.DB) *articleRepo
// ✅ return the biz interface, with a var _ assertion
```

## Related

- Domain errors and the sentinel mapping: `docs/guides/errors.md`
- Wiring constructors in `di.go`, assembly order of snowflake IDs and locks: `docs/guides/assembly.md`
- Test seams for repos (fakes): `docs/guides/testing.md`
- Calling downstream services, port interfaces in biz: `docs/guides/grpc-client.md`
- Event contracts: `docs/guides/mq.md`
- Decimal values in proto and their Go conversion: `keel:docs/proto.md#decimal-strings`,
  `keel:docs/utilities.md#decimal-strings-pkgtypes`
