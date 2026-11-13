# Adding or changing an HTTP or gRPC endpoint

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before adding an RPC (HTTP JSON or gRPC) or changing an endpoint's request, response or route: it covers the
whole path from the proto contract to the wiring in `di.go`.

The code below uses a fictional `Article` resource that this repository may not have; use your own resource name.

## Steps

1. **Proto contract**: add the rpc and messages under `api/<domain>/<version>/`. HTTP routes use `google.api.http`,
   field checks use `buf.validate`, anonymous endpoints carry `keel.v1.auth`
   (`keel:docs/proto.md`, `keel:docs/auth.md#anonymous-endpoints`).
   **In a service, declare an rpc with a fixed path before one whose path has a parameter in the same place**
   (`GET /v1/articles/export` before `GET /v1/articles/{id}`): HTTP routes are registered and matched in declaration
   order, so `{id}` declared first takes `export` and answers 400; gRPC is not affected, so only HTTP shows it.
2. **Generate**: `make proto` (also regenerates `openapi.yaml`). Done when it passes and nothing generated was edited
   by hand.
3. **Service layer**, `internal/service/<resource>.go`: the struct embeds `v1.Unsafe<Resource>ServiceServer`, so a
   method in the proto without an implementation fails to compile instead of answering "unimplemented" at run time
   (the proto and the service are generated and built together here, so gRPC's forward-compatibility reason for the
   `Unimplemented` variant does not apply); a method converts DTO → DO, calls biz, and builds the DTO inline at the
   return.
4. **Biz layer**, `internal/biz/<resource>.go`: the DO, the `<Resource>Repo` interface and the use case methods
   (domain logic, orchestration, locks, events).
5. **Data layer** (when the resource is persisted), `internal/data/<resource>.go`: implements `biz.<Resource>Repo`,
   the constructor returns the interface type, driver errors are mapped to domain errors.
6. **Server registration**: register with `v1.Register<Resource>ServiceHTTPServer(server, svc)` (gRPC likewise) in
   the register function and add the new service to its parameters. Register functions live in `internal/server/`
   unless this repository's AGENTS.md marks `internal/server/` as a read-only kernel; then put them where AGENTS.md
   says.
7. **Wire `di.go`**: in every `cmd/<app>/di.go` that serves the endpoint, construct in the order data → biz →
   service → server and insert the new constructors at their place.
8. **Error mapping**: a new domain error needs its reason, its sentinel and its mapping (`docs/guides/errors.md`).
9. Done when `make check` passes.

## Code patterns

### 1. Proto with HTTP routes

```protobuf
// api/<domain>/<version>/article.proto (example)
service ArticleService {
  rpc CreateArticle(CreateArticleRequest) returns (Article) {
    option (google.api.http) = {
      post: "/v1/articles"
      body: "*"
    };
  }

  // GetArticle returns one article by id. ARTICLE_NOT_FOUND when it does not exist.
  rpc GetArticle(GetArticleRequest) returns (Article) {
    option (google.api.http) = {
      get: "/v1/articles/{id}"
    };
    // Only on anonymous endpoints; by default sign-in is required (when the server's auth is on).
    option (keel.v1.auth) = {allow_anonymous: true};
  }

  rpc DeleteArticle(DeleteArticleRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {
      delete: "/v1/articles/{id}"
    };
  }
}

message GetArticleRequest {
  int64 id = 1 [(buf.validate.field).int64.gt = 0];
}
```

`GetArticle` and `CreateArticle` return the resource message `Article` directly, without a `GetArticleReply` /
`CreateArticleReply` wrapper: when the call has no extra fields to return, the wrapper buys nothing.

**Names: the resource in RPC and message names is singular, and reply messages end in `Reply`.** A collection endpoint
is `List<Resource>` with `List<Resource>Request` and `List<Resource>Reply` (`ListArticle`, `ListArticleRequest`,
`ListArticleReply`); a reply that does need its own message also ends in `Reply`, never `Response`. This matches the
Kratos service template, and singular names avoid English irregular plurals.
HTTP paths follow REST and use the plural (`/v1/articles`), independent of the RPC name.

HTTP responses are protojson (`keel:docs/http.md#json-encoding-is-protojson`); a plain frontend sends and receives
JSON with no special headers.

**An operation that only succeeds or fails (delete, disable, ...) returns `google.protobuf.Empty`**, as
`DeleteArticle` does: success is 2xx with `{}`, failure is an error. Do not invent an `ok` field that is always `true`.

**Messages shared by several proto files go in `types.proto` in the same directory**, not in one resource's file:
otherwise every user has to import an unrelated resource's contract.

#### List endpoints are paged by default

**A list that is shown page by page is paged on the server, always as `offset/limit` + `total`**, with sorting and
filtering pushed down to storage too. Each filter is its own field, a caller-chosen sort is an enum, and the row has
its own message:

```protobuf
message ListArticleRequest {
  int32 offset = 1 [(buf.validate.field).int32.gte = 0];
  // 0 means the server's default page size; the upper bound lives only here
  int32 limit = 2 [(buf.validate.field).int32 = {gte: 0, lte: 100}];
  // title keyword; empty means no filter
  string keyword = 3 [(buf.validate.field).string.max_len = 50];
  // pinned (assume articles have such a column): 0 all, 1 yes, 2 no
  int32 pinned = 4 [(buf.validate.field).int32 = {gte: 0, lte: 2}];
  Sort sort = 5 [(buf.validate.field).enum.defined_only = true];

  enum Sort {
    SORT_UNSPECIFIED = 0;     // server default: newest first
    SORT_TITLE_ASC = 1;
  }
}
message ListArticleReply {
  repeated ArticleListItem articles = 1;
  int64 total = 2;   // without it the caller's pager cannot compute pages and shows a single page
}
// One row of the list: only the fields the list shows, not the detail message Article
message ArticleListItem {
  int64 id = 1;
  string title = 2;
  bool pinned = 3;
  google.protobuf.Timestamp create_time = 4;
}
```

- **`total` is "how many match in all"**, independent of `offset/limit`. The data layer counts after filtering and
  before sorting and paging; it never loads the whole table to take `len()`.
- **Filter where you page.** A paged caller only holds the current page; filtering that page gives "the matches on this
  page", not "the first page of matches". Requests still succeed and pages still render, they just miss rows.
- **Each filter is its own field** (`keyword`, a related `xxx_id`, a status, ...) with its own `buf.validate` rule. No
  free-form `filter` string: it cannot be validated and callers cannot tell what it accepts.
- **Yes/no/all filters are `int32`: 0 all, 1 yes, 2 no**, validated `gte: 0, lte: 2`. Copying the column's 1/0 would
  make 0 mean both "all" and "no". In replies a yes/no is a plain `bool`.
- **The server fixes the sort by default.** When callers must choose, add an enum field `sort` (`SORT_UNSPECIFIED` is
  the server default) validated with `enum.defined_only`. No free-form `order_by` string: the enum limits sorting to
  the few orders an index supports.
- **The row has its own message** (`<Resource>ListItem`), never shared with the detail or Get message, even while the
  fields happen to match. Detail messages keep growing; a shared one quietly adds empty fields to the list, and sooner
  or later an empty field is read as "looked up, really empty". Splitting later changes the message name and the
  frontend's generated types, which costs more than splitting now.
- **The page-size limit lives only in the proto `lte` on `limit`**; the transport's validation rejects anything above
  it, and code does not clamp again. When `limit` is 0 or less, biz fills a default, a constant in biz chosen per
  resource; the service passes `offset` and `limit` through unchanged. Where entry validation never sees the request
  message (gRPC streaming RPCs, WebSocket; see `docs/guides/raw-http.md`), the handler validates the request itself and
  rejects anything out of range.

  ```go
  // internal/biz/<resource>.go
  const defaultArticlePageSize = 20

  func (uc *ArticleUseCase) ListArticle(ctx context.Context, opts ListOptions) ([]*Article, int64, error) {
      if opts.Limit <= 0 { // negative counts as "not given" too: gorm treats a negative Limit as no limit
          opts.Limit = defaultArticlePageSize
      }
      return uc.repo.List(ctx, opts)
  }
  ```
- Two exemptions, judged by the **shape and use of the data**: trees and hierarchies (paging would put children and
  parents on different pages), and option sets returned whole for a picker (fixed enumerations, or data known to stay
  small; an option you cannot page to cannot be picked, and the reply has no `total`). "The page renders it with a
  control that does not page" is not an exemption: that reasoning is circular, and any endpoint could exempt itself by
  switching controls.
- **An option set that keeps growing with the business is not exempt; it is fetched in batches by keyword**: the
  request has `keyword`, `offset`, `limit` and `repeated int64 ids`, the reply has `total`; an empty keyword returns the
  first batch; when `ids` is given, only those items come back (to show the names of selected items), ignoring keyword
  and paging. The batch limit is chosen per case and written on the `lte` of `limit`.
- **When the same data is both picker options and a managed list, make two endpoints**: `List<Resource>Options` for
  the options (shaped as above) and `List<Resource>` for the managed list. A paged list used as a picker cannot reach
  items past page one; an options-style list used for management breaks "a list shown page by page is paged on the
  server".

### 2. Service layer (the DTO ↔ DO boundary)

```go
// internal/service/<resource>.go
type ArticleService struct {
    v1.UnsafeArticleServiceServer

    uc *biz.ArticleUseCase
}

func NewArticleService(uc *biz.ArticleUseCase) *ArticleService {
    return &ArticleService{uc: uc}
}

func (s *ArticleService) GetArticle(ctx context.Context, req *v1.GetArticleRequest) (*v1.Article, error) {
    article, err := s.uc.GetArticle(ctx, req.GetId())
    if err != nil {
        return nil, err
    }
    return toProtoArticle(article), nil
}
```

On error the domain error is returned unchanged and sentinel maps it at the edge; on success `toProtoArticle`, in the
same file, converts DO → DTO.

### 3. Biz layer (DO, repo interface, use case)

The DO, the `<Resource>Repo` interface and the use case live in `internal/biz/<resource>.go`.

### 4. Data layer (PO and error mapping)

The PO, its `TableName()` and its conversions live in `internal/data/model.go`; the repo implementation in
`internal/data/<resource>.go`.

DO, repo interface and repo implementation: `docs/guides/domain-model.md`.

### 5. Server registration and di.go

```go
// internal/server/http.go (or where this repository's AGENTS.md puts registration): add the service and register it
func NewHTTPServer(article *service.ArticleService) api.HTTPServerRegister {
    return func(server *api.HTTPServer) {
        v1.RegisterArticleServiceHTTPServer(server, article)
    }
}
```

```go
// cmd/<app>/di.go, in the order data → biz → service → server
articleRepo := data.NewArticleRepo(db)
articleUseCase := biz.NewArticleUseCase(articleRepo)
articleService := service.NewArticleService(articleUseCase)
httpServerRegister := server.NewHTTPServer(articleService)
```

### 6. Error mapping

The three pieces for a new domain error are in `docs/guides/errors.md`. The one most often forgotten when adding an
endpoint is the mapping in `internal/service/errors.go`.

## Anti-patterns

- ❌ **Business logic or queries in the service layer**: the service only converts DTO ↔ DO and checks the boundary;
  rules and storage go down to biz and data.
- ❌ **Editing generated files**: `*.pb.go`, `*_http.pb.go` and `openapi.yaml` come only from `make proto`; patching
  `rawDesc` by hand panics at run time.
- ❌ **A list endpoint without paging, leaving the caller to fetch everything and page it**: it "works" until the data
  grows, and by then the pattern has spread to every list page.
- ❌ **A home-made paging shape** (`page/page_size`, `pageNum/pageSize`, cursor tokens, ...): the backend converts it to
  `offset` anyway, so the wrapper buys nothing and every caller writes its own adapter.
- ❌ **Free-form `filter` / `order_by` strings on a list request**: the server needs its own parser, entry validation
  cannot check them, and callers cannot tell what they accept. Filters are fields, sorting is an enum.
- ❌ **A list reply of `repeated <Resource>`, sharing the detail message**: every detail field added becomes a field
  that is always empty in the list.
- ❌ **Clamping `limit` again or filling its default in the service**: the bound belongs to the proto `lte`, the default
  to biz.

## Related

- `docs/guides/errors.md`: domain errors and the sentinel mapping
- `docs/guides/domain-model.md`: DO/PO split and GORM model rules
- `docs/guides/raw-http.md`: webhook, SSE and download endpoints outside proto
- `docs/guides/assembly.md`: assembly order in `di.go` and cleanups
- `keel:docs/http.md`, `keel:docs/grpc.md`, `keel:docs/middleware.md`: servers and middleware
