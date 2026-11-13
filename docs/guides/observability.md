# Logs, traces and metrics

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before writing business logs, tracing a problem, changing the `logger` / `otel` config, enabling request
body logging, or passing metadata between services.

How logging, tracing and metrics are set up and what Keel instruments is in
[Observability and logging](keel:docs/observability.md); this guide covers how this service uses them.

## Rules

Logging rules and levels: [Logging](keel:docs/observability.md#logging).

1. **Log an error once, where it is wrapped.** The data layer logs when it wraps an unexpected driver error into
   `ErrUnexpectedError`; upper layers pass it on without logging again (`docs/guides/errors.md`).
2. **Pass `ctx` down, including into goroutines.** A goroutine started with `context.Background()` breaks the trace
   and is not cancelled with the request. Work that must finish after the request returns uses
   `context.WithoutCancel(ctx)` (see `docs/practices/go.md`); a long-running loop uses the `worker.Server` ctx.
3. **Request and response bodies are not logged by default** (personal data). To investigate, enable them where the
   chain is built and remove that again afterwards:

   ```go
   import logging "github.com/go-keel/keel/middleware/logging"

   mw.WithLogging(chain,
       logging.WithLogRequest(true),
       logging.WithLogResponse(true),
       logging.WithLogHeaders(true), // credential headers are redacted
   )
   ```

   Options: [Logging](keel:docs/middleware.md#logging).

## Configuration

`configs/config.yaml` has a `logger` block (level, stack trace level, console output and format) and an `otel` block
(collector endpoint). Add `trace_sample_ratio` to sample; use a ratio around 0.1 in production. Keys and defaults:
[Config](keel:docs/observability.md#config). An unreachable collector does not stop startup, so missing telemetry
means checking the collector, not the startup log.

## Traces and metrics

Requests, database and Redis calls, events, tasks and calls to other services are traced without business code, as
long as `ctx` is passed down ([Tracing](keel:docs/observability.md#tracing)). Request count and latency come from
the metrics slot of `mw.Default`. For finer spans or business metrics, use the OpenTelemetry globals
(`otel.Tracer(...)`, `otel.GetMeterProvider()`); they are process facilities, never passed through `di.go`.

## Metadata between services

- **Client IP:** `metadata.GetRemoteIP(ctx)`; in a raw route or pre-routing filter, pass `r.Context()`. Never read
  `X-Forwarded-For` or `X-Real-IP` yourself. It is for logs and audit only; deployment is in
  `docs/guides/deploy-nginx.md`.
- **Your own metadata:** headers with the `x-keel-` prefix are forwarded between services; read them in
  `internal/service/` with `metadata.FromServerContext(ctx)`.
- **Inbound `x-keel-*` values are the caller's claims, not facts.** A trusted value is written by the entry service
  after authentication, overwriting whatever came in. Trust model:
  [Metadata and the client IP](keel:docs/metadata.md).

## Related

- `docs/guides/assembly.md`: building the middleware chain
- `docs/guides/errors.md`: who logs an error and who wraps it
- `docs/guides/mq.md`, `docs/guides/task.md`: consumers and handlers receive a `ctx` that continues the trace
