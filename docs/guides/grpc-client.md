# Calling a downstream gRPC service

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before calling another service's gRPC (or HTTP) endpoints: building the client, its config, its chain,
service-to-service tokens and the wiring in `di.go`.

What `keel.NewServiceClient` does, its config, the default client chain and how errors flow are in
`keel:docs/client.md`. This guide covers where each piece goes here.

## Steps

1. **Get the downstream proto stubs**: `go get` the downstream service's api module (or use a package under this
   repository's `api/<domain>/<version>`) to get `xxxpb.NewXxxServiceClient`.
2. **Config**: add an entry to the `client` block in `configs/config.yaml`, keyed by the service name (such as
   `user-svc`), with `endpoint` and `timeout`.
3. **Client constructor**: in internal/data/client.go (create it), built on
   `keel.NewServiceClient(factory, name, newFn, chain)`.
4. **Port interface in biz**: to biz, a call to another service is an external port. Declare the interface in
   `internal/biz/` (in `biz/<domain>.go` for one domain, `biz/ports.go` when shared across domains); it takes and
   returns DOs.
5. **Port implementation in data**: in `internal/data/`, wrap the pb client and convert pb ↔ DO; the downstream's
   Kratos errors **pass through unchanged**.
6. **Wire `di.go`**: `keel.NewServiceClientFactory` → each client constructor → the port implementation → the biz use
   case, with every cleanup appended to `cleanups`.
7. Done when `make check` passes.

## Code patterns

### 1. Config (configs/config.yaml)

```yaml
client:
  user-svc:
    endpoint: "dns:///user-svc:9000"
    timeout: 5s
  payment-svc:
    endpoint: "dns:///payment-svc:9000"
    timeout: 10s
```

`timeout` is per RPC. The connection is lazy: building the client neither dials nor blocks startup, so a bad endpoint
shows up on the first call.

### 2. Client constructor (internal/data/client.go)

```go
package data

import (
    "github.com/go-keel/keel"

    userpb "example.com/user-svc/api/user/v1"
)

// chain nil means mw.ClientDefault
func NewUserServiceClient(factory *keel.ServiceClientFactory) (userpb.UserServiceClient, func(), error) {
    return keel.NewServiceClient(factory, "user-svc", userpb.NewUserServiceClient, nil)
}
```

The service name must match the key under `client` in the config. The default client chain already has the circuit
breaker and no request logging (the server side logs, tracing links the two); see
`keel:docs/client.md#the-client-chain`.

- Service-to-service tokens: build the chain and add `mw.WithClientAuth(chain, tokenFn)`
  (`keel:docs/auth.md#calling-another-service-with-a-token`).
- Your own client middleware: `InsertBefore` / `InsertAfter` on a `mw.ClientSlot*` anchor, as on the server chain.

### 3. Port in biz, adapter in data (keeps the layers apart)

```go
// internal/biz/ports.go: biz knows DOs only and does not import downstream pb
type UserPort interface {
    GetUser(ctx context.Context, userID int64) (*User, error)
}
```

```go
// internal/data/user_port.go
type userPort struct {
    cli userpb.UserServiceClient
}

var _ biz.UserPort = (*userPort)(nil)

func NewUserPort(cli userpb.UserServiceClient) biz.UserPort {
    return &userPort{cli: cli}
}

func (p *userPort) GetUser(ctx context.Context, userID int64) (*biz.User, error) {
    resp, err := p.cli.GetUser(ctx, &userpb.GetUserRequest{Id: userID})
    if err != nil {
        return nil, err // already a Kratos error from the downstream → pass through; sentinel lets it out
    }
    return &biz.User{ // pb → DO
        ID:   resp.GetId(),
        Name: resp.GetName(),
    }, nil
}
```

### 4. Wiring (cmd/<app>/di.go)

```go
// between the infrastructure and the data layer
clientFactory := keel.NewServiceClientFactory(keel.NewConfig(config))

userClient, userClientCleanup, err := data.NewUserServiceClient(clientFactory)
if err != nil {
    return nil, cleanup, err
}
cleanups = append(cleanups, userClientCleanup) // closes the connection at shutdown (LIFO)

userPort := data.NewUserPort(userClient)
orderUseCase := biz.NewOrderUseCase(orderRepo, userPort)
```

Biz passes the error through as well:

```go
func (uc *OrderUseCase) PlaceOrder(ctx context.Context, userID int64) error {
    user, err := uc.userPort.GetUser(ctx, userID)
    if err != nil {
        return err // a gRPC error passes through; never wrap it again
    }
    slog.InfoContext(ctx, "user verified", "user_id", user.ID)
    // ...
}
```

## When the downstream serves only HTTP

The gRPC client above stays the first choice. When the downstream exposes only HTTP, or this service is outside the
service network, build a client with `khttpclient.New` (once in `di.go`, cleanup appended as usual) and still implement
the biz port in `internal/data/` (`keel:docs/http-client.md`):

- With the downstream's pb package: decode with `SetResult(khttpclient.ProtoResult(&resp))`, never a generated type
  directly or your own `protojson.Unmarshal` ([Which path](keel:docs/http-client.md#which-path)).
- Without a pb package: write the struct by hand following
  `keel:docs/http-client.md#response-field-shapes`.
- Check business errors with `khttpclient.IsReason(err, "REASON")` and do not wrap them again (as in
  `docs/guides/errors.md`); what a 2xx `Code` means for retries: [Errors](keel:docs/http-client.md#errors).

## Anti-patterns

- ❌ **`SetResult` on a generated pb type, or reading `Body` and calling `protojson.Unmarshal`**: the first cannot
  read protojson, the second easily misses `DiscardUnknown`. Use `khttpclient.ProtoResult`.
- ❌ **Biz holding the pb client or importing the downstream pb package**: the downstream's proto is a DTO to this
  service; skipping the port breaks the layering and testability (a port is easy to fake, a pb client is not).
- ❌ **Wrapping downstream errors again**: see "Wrapping at a glance" in `docs/guides/errors.md`.
- ❌ **Dropping the cleanup** (`_, _, _ := keel.NewServiceClient(...)`, or never appending it): the connection is not
  closed at shutdown.
- ❌ **Bypassing the factory with `grpc.NewClient` or a bare Kratos client**: it loses the default chain's tracing,
  metrics and circuit breaker and the shared config.
- ❌ **Building a client per request**: build it once in `di.go` and reuse it; rebuilding on the request path is slow
  and leaks cleanups.
- ❌ **A service name that differs from the config key**: `NewServiceClient` looks up `client.<name>` and fails to
  build when it is missing.

## Related

- `docs/guides/assembly.md`: assembly order in `di.go` and cleanups
- `docs/guides/errors.md`: passing errors through and the sentinel fallback
- `docs/guides/api-endpoint.md`: serving gRPC endpoints from this service
- `keel:docs/client.md`, `keel:docs/http-client.md`: the clients and choosing between them
