# HTTP endpoints beyond request/response: streams, downloads, webhooks

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before exposing SSE, a WebSocket, a file download, a third-party webhook or a pre-routing filter on an HTTP
server.

The two kinds of endpoints (contract endpoints declared in proto, raw routes that skip the middleware chain), the
stream and download helpers and their guarantees are in [HTTP server](keel:docs/http.md); this guide covers how
this service writes them.

## Choosing

**Prefer a contract endpoint.** It runs the whole middleware chain, including any middleware this service adds
(permission checks, audit); a raw route runs none of it. Use a raw route only for what proto cannot express: raw
bytes, free-form frames, Range.

| Case | Contract endpoint (runs the chain) | Raw route (skips the chain) |
|---|---|---|
| server push (SSE) | 1b server-streaming RPC | 2a `api.SSEHandler` |
| bidirectional stream (WebSocket) | 1c bidi-streaming RPC | 2b `api.WebSocketHandler` |
| file or binary response | 1d RPC returning `google.api.HttpBody` | 2c `api.ServeContent` (Range, resume) |
| raw body (signed callback) | none: the decoded body cannot rebuild the signed bytes | 2d `sc.Server()`, verifies itself |
| signature over contract endpoints | 2e `sc.Filter`: verify before routing, put the body back, let the endpoint decode | |

The chain runs once per stream, so on 1c and gRPC streams validate each message in the handler
([Two kinds of endpoints](keel:docs/http.md#two-kinds-of-endpoints)). Small exports go through 1d; large files and
Range through 2c.

## Steps for a raw route

1. Write the handler in `internal/service/` as a thin protocol adapter; business logic stays in biz.
2. Register it in the stream register function (returns `api.StreamRegister`; by default in `internal/server/`, or
   where this repository's AGENTS.md says if that directory is read-only). Every registration picks one of
   `sc.RequireLogin()`, `sc.Anonymous()` or `sc.Server()` ([Raw routes](keel:docs/http.md#raw-routes));
   `sc.Server()` only for a route that authenticates itself, with a comment saying so.
3. Pass the register as the last argument of `api.NewHTTPServer` in the entry point, and wire the handler's
   dependencies in `cmd/<app>/di.go`.
4. Pick a path no contract endpoint uses: on a shared path the contract endpoint wins
   ([Raw routes](keel:docs/http.md#raw-routes)). If this repository's AGENTS.md reserves prefixes (the root for
   a front end, say), follow it. Done when the route answers and `make check` passes.

```go
func NewStreamRegister(streamSvc *service.StreamService, webhook *service.WebhookHandler) api.StreamRegister {
    return func(sc *api.StreamContext) {
        sc.Anonymous().HandleFunc("/v1/articles/feed", streamSvc.ArticlesSSE())
        sc.RequireLogin().HandleFunc("/v1/echo", service.WSEchoHandler())
        sc.RequireLogin().HandleFunc("/v1/reports/download", service.ReportDownloadHandler(openReport))
        sc.Server().HandleFunc("/v1/hooks/payment", webhook.HandlePayment) // verifies its own HMAC signature
        sc.Filter(webhook.SignatureFilter("/v1/partner/")) // 2e: verify before the contract endpoints decode
    }
}

// in newApp
apiServer, err := api.NewHTTPServer(httpCfg, apiRegister, chain, streamRegister)
```

## What a raw route does itself

Nothing in the chain protects a raw route, so each one writes these:

- **A method gate with an `Allow` header on 405.** Code generation limits methods only for proto routes. SSE accepts
  `GET` only; downloads `GET` and `HEAD` (HEAD probes size before a resumed download); webhooks `POST` only. WebSocket
  needs none: the upgrader rejects anything but a GET upgrade request (400 without the upgrade headers, 405 for
  another method with them).
- **A body limit with `http.MaxBytesReader`, answering 413.** `io.LimitReader` silently truncates an oversized body,
  which then fails signature checks with a misleading 401.
- **The client IP from `metadata.GetRemoteIP(r.Context())`.** Never read `X-Forwarded-For` / `X-Real-IP`: anyone can
  write them.
- **Fail closed on a missing secret.** The webhook secret is empty in the repository (no default secret anyone can
  read); an empty secret answers 503 instead of verifying against it.

## Code

The paths and the `Article` resource are examples.

### 2a SSE

```go
func (s *StreamService) ArticlesSSE() http.HandlerFunc {
    sse := api.SSEHandler(func(ctx context.Context, w *api.SSEWriter) error {
        articles, _, err := s.uc.ListArticle(ctx, biz.ListOptions{Limit: 100})
        if err != nil {
            return err
        }
        for _, article := range articles {
            data, err := json.Marshal(article)
            if err != nil {
                return err
            }
            if err := w.WriteEvent(api.SSEEvent{Event: "article", Data: string(data)}); err != nil {
                return err
            }
        }
        return nil // returning ends the stream; to keep pushing, select on ctx.Done() and a business channel
    })

    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet {
            w.Header().Set("Allow", http.MethodGet)
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }
        sse(w, r)
    }
}
```

Log tailing and progress pushes use the same helper: subscribe in the source and write one event per line.

### 2b WebSocket

```go
func WSEchoHandler() http.HandlerFunc {
    return api.WebSocketHandler(func(ctx context.Context, conn *websocket.Conn) error {
        for {
            mt, data, err := conn.ReadMessage()
            if err != nil {
                return err // a client disconnect counts as a normal end
            }
            if err := conn.WriteMessage(mt, data); err != nil {
                return err
            }
        }
    }, api.WSOptions{ReadLimit: 1 << 20}) // give CheckOrigin an allowlist for cross-origin use
}
```

### 2c Download

```go
func ReportDownloadHandler(open func(r *http.Request) (io.ReadSeeker, error)) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet && r.Method != http.MethodHead {
            w.Header().Set("Allow", "GET, HEAD")
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }
        content, err := open(r) // object storage or a local file
        if err != nil {
            http.Error(w, "file not found", http.StatusNotFound)
            return
        }
        api.ServeContent(w, r, content, api.DownloadOptions{
            Filename:    "report.txt",
            ContentType: "text/plain; charset=utf-8",
        })
    }
}
```

A plain stream copy without Range uses `api.ServeReader`.

### 2d Webhook signature

The secret comes from the hot-reloaded app config; how to add the field is in `docs/guides/config.md`.

```go
// maxWebhookBody caps the callback body: a raw route has no chain to limit it.
const maxWebhookBody = 1 << 20 // 1 MiB

func (h *WebhookHandler) HandlePayment(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.Header().Set("Allow", http.MethodPost)
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
    body, err := io.ReadAll(r.Body)
    if err != nil {
        var tooLarge *http.MaxBytesError
        if errors.As(err, &tooLarge) {
            http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
            return
        }
        http.Error(w, "read body failed", http.StatusBadRequest)
        return
    }
    secret := h.bootstrap.Get().GetCustom().GetWebhookSecret()
    if secret == "" { // fail closed
        http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
        return
    }
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(body)
    if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("X-Signature"))) {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }
    // decode the body, call a biz UseCase
    w.WriteHeader(http.StatusOK)
}
```

The signature is computed over the raw bytes, which is why this is a raw route. A callback with a fixed shape and no
signature is a proto endpoint (1a).

### 2e Pre-routing filter

When the signature must cover contract endpoints, 2d is not enough: they decode the body before the chain sees it.
Mount a filter with `sc.Filter` ([2e](keel:docs/http.md#2e-a-filter-over-the-whole-port)); it runs after the
client IP is computed and before routing.

```go
func (h *WebhookHandler) SignatureFilter(prefix string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Sees every request on the port, /health included: pass through what it does not own, unread.
            if !strings.HasPrefix(r.URL.Path, prefix) {
                next.ServeHTTP(w, r)
                return
            }
            body, ok := h.readVerified(w, r) // as in 2d: MaxBytesReader, 503 on an empty secret, HMAC
            if !ok {
                return
            }
            r.Body = io.NopCloser(bytes.NewReader(body)) // put it back, or the endpoint decodes nothing
            next.ServeHTTP(w, r)
        })
    }
}
```

A filter does only what must happen before decoding; what runs around it and what it must not do:
[2e: a filter over the whole port](keel:docs/http.md#2e-a-filter-over-the-whole-port).
- Never wrap the server's `Handler` after `NewHTTPServer` instead: that runs before the client IP is computed.

## Related

- `docs/guides/api-endpoint.md`: contract endpoints (1a)
- `docs/guides/auth.md`: `RequireLogin` / `Anonymous` / `Server`
- `docs/guides/assembly.md`: wiring in `di.go`
