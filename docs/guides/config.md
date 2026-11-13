# Configuration (framework config, app Bootstrap, reload)

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before adding or changing any setting: framework config (`configs/config.yaml`), this service's own config
(`internal/conf/app.proto` + `configs/app.yaml`), local overrides or environment overrides.

How Keel loads, layers, validates and reloads both files is in `keel:docs/config.md`. This guide covers which file
a setting goes in and how to add one here.

The repository has two configs with different jobs:

| | Framework config | Service config |
|---|---|---|
| File | `configs/config.yaml` (+ `config.dev.yaml` locally) | `configs/app.yaml` (+ `app.dev.yaml` locally) |
| Schema | Keel's `kconfig.Config` (fixed) | `conf.Bootstrap`, generated from `internal/conf/app.proto` |
| Loaded by | `kconfig.NewConfig()` in `di.go` | `kconfig.NewDynamic[conf.Bootstrap]("configs/app.yaml")` in `main.go` |
| Reload | no (database, Redis, NATS changes need a restart) | yes (`Get()` returns the latest file contents) |
| Holds | server, database, redis, nats, asynq, client, logger, otel | business switches, thresholds, timeouts |

`config.yaml` and `app.yaml` are the **real production config**; `config.dev.yaml` and `app.dev.yaml` are local
overlays, never committed, skipped when absent. **For local work edit the `.dev.yaml` file, not the committed
`config.yaml` / `app.yaml`.**

## Rules for this repository

- **Secrets** (`jwt_secret`, database passwords, ...) never go into the committed `configs/config.yaml`. Locally they
  go in `config.dev.yaml` (`configs/config.dev.example.yaml` is the template); in production the deployment puts the
  real values in `config.yaml` (`keel:docs/config.md#secrets`).
- **A `KEEL_` environment variable fills a `${KEY}` placeholder**: write `${JWT_SECRET}` in the nested field of
  `config.yaml` and set `KEEL_JWT_SECRET`; it does not reach a nested key by name. Local literal values go in
  `config.dev.yaml` (`keel:docs/config.md#environment-overrides-keel_-prefix`).
- **The main config path is fixed** at `configs/config.yaml` in the working directory: the deployment mounts the real
  config there (in Kubernetes, the ConfigMap or Secret mountPath).
- **Unused sections are validated too**, and a failing config stops startup with every failing key listed: do not
  leave `database.<name>` or `redis.<name>` sections that no entry point assembles
  (`keel:docs/config.md#validation`).
- **The dev overlay warning is an incident signal in production**: `kconfig: dev overlay loaded` means a `.dev.yaml`
  reached a deployment, and `app.yaml` usually holds secrets (`keel:docs/config.md#declaring-production-keel_env`).

## Steps: adding a field to the service config

1. Add the field to a message in `internal/conf/app.proto` (snake_case).
2. Run `make config` to regenerate `internal/conf/app.pb.go`; never edit it by hand.
3. Give it a value in `configs/app.yaml`.
4. Read it through the injected `*kconfig.Dynamic[conf.Bootstrap]` with `.Get()` at the point of use.

Done when `make check` passes and the process starts with no `kconfig` error.

## Code patterns

### The framework config (configs/config.yaml)

Top-level blocks: `app`, `logger`, `otel`, `server`, `client` (map), `database` (map), `redis` (map), `nats` (map),
`asynq` (map). Resource blocks are maps of named instances, usually keyed `default`; `server.http` and `server.grpc`
are maps too (`default`, `public`, `admin`, ...), each server with its own `auth`. The HTTP server's `client_ip`
(which proxies to trust for the client IP) is covered in `docs/guides/deploy-nginx.md`. Every field and its default
is in `keel:docs/config.md#the-framework-config-configyaml`.

```yaml
app:
  domain: keel
  name: my-service
  version: 1.0.0
  node_id: 0                # snowflake node: 0-1023 fixed, -1 allocated through Redis
  stop_timeout: 20s         # shutdown budget for all servers, including the asynq worker

server:
  http:
    default:
      addr: "0.0.0.0:8080"
      timeout: 10s
      auth:
        enabled: false
  grpc:
    default:
      addr: "0.0.0.0:9000"
      timeout: 10s

client:                      # downstream gRPC services
  account:
    endpoint: "localhost:9001"
    timeout: 5s

database:
  default:
    driver: mysql
    source: user:pass@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=UTC&time_zone=%27%2B00%3A00%27

redis:
  default:
    addr:
      - 127.0.0.1:6379
```

Code picks a named instance: `c.Server.Http["default"]`, `kdb.NewNamedDB(config, "analytics")`, ... (see
`docs/guides/assembly.md`).

### The service config: app.proto → make config → Dynamic

A teaching example; choose fields for your own needs. When `Bootstrap` already has other branches (for example one an
upstream template defined and reads once at startup), a new branch takes the next free field number and its own
message; do not put your settings into someone else's branch:

```proto
// internal/conf/app.proto
message Bootstrap {
  Custom custom = 1;
}

message Custom {
  google.protobuf.Duration timeout = 1;
  bool enable_new_feature = 2;
  // webhook signing key (HMAC-SHA256); the raw-http guide's webhook check reads this field
  string webhook_secret = 3;
}
```

```yaml
# configs/app.yaml (reloads)
custom:
  timeout: 30s
  enable_new_feature: false
  # Empty on purpose: no default key everyone can see sits in the repository; when it is missing the
  # handler answers 503 instead of verifying with it. Production injects it from secret management.
  webhook_secret: ""
```

**The file must be named `app.proto`, never `conf.proto`**: Keel already registers `conf.proto` in the global proto
registry, and a second file with that name panics at startup (`file conf.proto is already registered`).

**`T` in `kconfig.NewDynamic[T]` is the type generated from `app.proto`**, never a hand-written Go struct: YAML keys
are snake_case, and a struct without matching tags silently reads them as zero values; the generated type maps them
through protojson.

### Loading and reading

```go
// cmd/<app>/main.go: loaded once at process start
bootstrap, cleanup, err := kconfig.NewDynamic[conf.Bootstrap]("configs/app.yaml")
if err != nil {
	panic(err)
}
defer cleanup()

app, appCleanup, err := wireApp(bootstrap) // injected into the constructors that need it
```

A missing `configs/app.yaml` fails startup; in Kubernetes make sure the ConfigMap is mounted at that path.

```go
// internal/service/<resource>.go (example): inject the Dynamic itself; call Get() at run time for the latest value
type WebhookHandler struct {
	bootstrap *kconfig.Dynamic[conf.Bootstrap]
}

func NewWebhookHandler(bootstrap *kconfig.Dynamic[conf.Bootstrap]) *WebhookHandler {
	return &WebhookHandler{bootstrap: bootstrap}
}

func (h *WebhookHandler) HandleExample(w http.ResponseWriter, r *http.Request) {
	// A missing key fails closed: the repository holds no usable default, so a missing setting refuses
	// service instead of verifying with a value everyone can see.
	secret := h.bootstrap.Get().GetCustom().GetWebhookSecret()
	if secret == "" {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	// ...
}
```

- A change takes effect only where code calls `Get()` at run time.
- `Get()` returns a snapshot: within one request, reuse the same snapshot so related values stay consistent.

## Anti-patterns

- ❌ A hand-written Go struct as `T` of `kconfig.NewDynamic[T]`, or a config proto named `conf.proto`
  ([Your service config](keel:docs/config.md#your-service-config-appyaml)). `T` comes from `app.proto` +
  `make config`.
- ❌ Calling `Get()` once in the constructor and caching fields in the struct: reload stops working. Inject
  `*kconfig.Dynamic[T]` and call `Get()` when needed.
- ❌ Committing `jwt_secret` or database passwords in `configs/config.yaml`: dev secrets go in `config.dev.yaml`
  (gitignored), production secrets come from the deployment.
- ❌ Expecting a `KEEL_` variable to set a nested field by name: put a `${KEY}` placeholder in the field, or use
  `config.dev.yaml` locally.
- ❌ Editing `internal/conf/app.pb.go` or other generated files: change `app.proto` and run `make config`.
- ❌ Business switches in the framework `config.yaml`: they go in `app.proto` + `app.yaml`, which reload and are typed.

## Related

- Where `kconfig.NewConfig` and `NewDynamic` are called and injected: `docs/guides/assembly.md`
- The auth block: `docs/guides/auth.md`
- The nginx in front of the service and `client_ip`: `docs/guides/deploy-nginx.md`
- `keel:docs/config.md`: loading order, validation rules, dev overlays, reload guarantees
