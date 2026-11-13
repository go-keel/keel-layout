# JWT authentication

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before turning on JWT authentication, marking an endpoint anonymous, reading the current user, issuing
tokens, adding a check after authentication, or calling another service with a token.

Each server builds its own authentication from its `auth` config section; the mechanism, the error responses and
what fails startup are in `keel:docs/auth.md`. This guide covers where things go in this repository.

## Steps

1. Turn on `auth` for the server in `configs/config.yaml` (each HTTP and gRPC server has its own block). Secrets never
   go into the repository: locally they go in `configs/config.dev.yaml`, in production the deployment supplies them
   (`docs/guides/config.md`).
2. Mount nothing: pass the chain from `mw.Default(c.Config)` to the server constructor unchanged in `newApp`; the
   server fills the auth slot itself.
3. Mark sign-in-free endpoints in the `.proto` with `option (keel.v1.auth) = {allow_anonymous: true};`, then
   `make proto`. No code registers them.
4. Read the current user in `internal/service/` with `auth.FromContext(ctx)` and pass the user ID to biz as a plain
   parameter. **Biz never sees claims.**
5. Issue tokens (in the service that handles sign-in) with `jwt.NewClaims(subject, ttl)` and `jwt.NewRS256Signer` /
   `jwt.NewHS256Signer` (`keel:docs/utilities.md#jwt-pkgjwt`).
6. Checks on the claims (bans and the like) are your own middleware inserted after the auth slot; authentication only
   verifies and identifies (see "Checks after authentication").
7. Calls to other gRPC services carry a token through `mw.WithClientAuth(chain, tokenFn)` on the client chain. Raw
   routes choose `sc.RequireLogin()` or `sc.Anonymous()` (`docs/guides/raw-http.md`).

Done when `make check` passes and a request without a token to a protected endpoint gets 401.

## Code patterns

### Configuration

```yaml
# configs/config.yaml: one auth block per server
server:
  http:
    default:
      addr: "0.0.0.0:8080"
      timeout: 10s
      auth:
        enabled: true
        algorithm: RS256                        # RS256 (default) | HS256
        jwt_public_key_file: "keys/public.pem"  # RS256: the public key verifies
        # jwt_secret: "..."                     # HS256: never committed; config.dev.yaml locally, deployment in prod
```

### Anonymous endpoints in proto

```proto
import "keel/v1/annotations.proto";

service AccountService {
  // sign-in required (the default)
  rpc GetProfile(GetProfileRequest) returns (Profile) {}

  // no sign-in
  rpc Login(LoginRequest) returns (LoginReply) {
    option (keel.v1.auth) = {allow_anonymous: true};
  }
}
```

### The current user (service layer)

```go
import "github.com/go-keel/keel/middleware/auth"

func (s *AccountService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.Profile, error) {
    claims, ok := auth.FromContext(ctx)
    if !ok {
        return nil, biz.ErrInvalidArgument // should not happen: non-anonymous endpoints are blocked before this
    }
    userID, _ := claims["sub"].(string)

    profile, err := s.uc.GetProfile(ctx, userID) // biz takes plain parameters only
    if err != nil {
        return nil, err
    }
    return &pb.Profile{ /* ... */ }, nil
}
```

### Issuing a JWT

```go
import "github.com/go-keel/keel/pkg/jwt"

// at assembly: load the private key and build the signer (RS256)
priv, err := jwt.LoadRSAPrivateKey("keys/private.pem")
if err != nil {
    return nil, err
}
signer := jwt.NewRS256Signer(priv)
// HS256: signer, err := jwt.NewHS256Signer([]byte(secret))

// in the sign-in use case: NewClaims sets sub/iat/exp with the right types; then add your own claims
claims := jwt.NewClaims(strconv.FormatInt(userID, 10), 24*time.Hour)
claims["sid"] = sessionID
claims["role"] = "admin"

token, err := signer.Sign(claims)
```

### Checks after authentication

Authentication answers who the caller is, not whether they may act (bans, a revoked session, ...). Such a check is
your own middleware inserted with `InsertAfter(mw.SlotAuth, ...)`, reading the claims through `auth.FromContext`:

```go
func principalCheck() mw.Middleware {
    return func(next middleware.Handler) middleware.Handler {
        return func(ctx context.Context, req any) (any, error) {
            claims, ok := auth.FromContext(ctx)
            if ok {
                if _, banned := claims["banned"]; banned {
                    return nil, biz.ErrUnauthenticated // this repository's own sentinel, mapped to 401
                }
            }
            return next(ctx, req)
        }
    }
}

// cmd/<app>/main.go, where newApp builds the chain
chain := mw.Default(c.Config)
chain.InsertAfter(mw.SlotAuth, principalCheck(), "principal")
```

An anonymous endpoint called without credentials gives `ok=false`, so this lets it through; the check runs only when
there are claims. Return a sentinel whose message says nothing about why: authentication failures never reveal
verification details, and this check must not either.

### Calling another service (gRPC client)

```go
chain := mw.ClientDefault(c.Config)
mw.WithClientAuth(chain, func() string {
    return internalToken() // a valid JWT; sent as Authorization: Bearer
})
```

### Raw routes

Raw routes (SSE, downloads, webhooks, ...) skip the chain and declare authentication when registered in the
`streamRegister` callback: `sc.RequireLogin()` or `sc.Anonymous()`, and `sc.Server()` only for routes that skip
framework authentication on purpose (a webhook with its own signature check). Claims are read the same way, with
`auth.FromContext(ctx)`. See `docs/guides/raw-http.md`.

## Anti-patterns

- ❌ Writing `exp` by hand as `int64` (`time.Now().Add(ttl).Unix()`): `Sign` / `Verify` fail with `invalid type for
  claim`. Use `jwt.NewClaims(id, ttl)`.
- ❌ Passing claims or the auth context into biz (`GetProfile(ctx context.Context, claims jwtv5.MapClaims)`): the
  service extracts the user ID; biz takes business parameters only.
- ❌ Committing `jwt_secret` in `configs/config.yaml`: locally it goes in `config.dev.yaml` (gitignored), in production
  the deployment supplies it.
- ❌ Skipping auth in code (`if path == "/login"`): mark the endpoint with `option (keel.v1.auth) =
  {allow_anonymous: true}`.
- ❌ Registering a raw route with `sc.Server()` to save a line: routes that need sign-in use `sc.RequireLogin()`;
  `sc.Server()` is only for routes that do their own verification.

## Related

- Chain slots and inserting your own middleware: `keel:docs/middleware.md#adding-your-own-middleware`
- Raw routes: `docs/guides/raw-http.md`
- Authentication errors and sentinel: `docs/guides/errors.md`
- `keel:docs/auth.md`: configuration, anonymous endpoints, cookies, client tokens
