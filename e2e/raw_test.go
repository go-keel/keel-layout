//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/go-keel/keel-layout/e2e/harness"
)

// writeAppYAML replaces the app.yaml of a process's working directory.
func writeAppYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "configs", "app.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func secretYAML(secret string) string { return "custom:\n  webhook_secret: \"" + secret + "\"\n" }

// postWebhook posts body to /examples/webhook of base signed with secret ("" sends no signature).
func postWebhook(t *testing.T, base, secret, body string, headers ...string) response {
	t.Helper()
	if secret != "" {
		headers = append(headers, "X-Signature", sign(secret, body))
	}
	return call(t, http.MethodPost, base+"/examples/webhook", body, headers...)
}

// setWebhookSecret writes secret into the running api's app.yaml and waits until the reload takes effect.
func setWebhookSecret(t *testing.T, base string, p *harness.Process, secret string) {
	t.Helper()
	writeAppYAML(t, p.Dir, secretYAML(secret))
	waitFor(t, "webhook secret reloaded", func() bool {
		return postWebhook(t, base, secret, "{}").code == http.StatusOK
	})
}

// waitFor polls cond until it holds, failing the test after the default wait.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitWithin(t, timeout, what, cond)
}

// waitWithin polls cond until it holds, failing the test after d.
func waitWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, d)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Basis: keel:docs/http.md, "2a" (raw SSE); docs/guides/raw-http.md, "What a raw route does itself".
func testRawSSE(t *testing.T, e *env) {
	e.ensure(t, 1)
	r := e.call(t, http.MethodGet, "/examples/sse", "").want(t, 200)
	for h, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if !strings.HasPrefix(r.header.Get(h), want) {
			t.Fatalf("%s = %q, want %q", h, r.header.Get(h), want)
		}
	}
	events := strings.Count(string(r.body), "event: todo\n")
	if int64(events) != min(e.count(t), 100) || len(sseData(r.body)) != events {
		t.Fatalf("%d todo events, %d data lines, database %d", events, len(sseData(r.body)), e.count(t))
	}
	if r := e.call(t, http.MethodPost, "/examples/sse", "").want(t, 405); r.header.Get("Allow") != "GET" {
		t.Fatalf("Allow %q", r.header.Get("Allow"))
	}
}

// Basis: keel:docs/http.md, "2b" (raw WebSocket; a frame over ReadLimit closes with 1009 and logs a warning);
// docs/guides/raw-http.md, "What a raw route does itself".
func testRawWebSocket(t *testing.T, e *env) {
	c := e.dialWS(t, "/examples/ws")
	for _, mt := range []int{websocket.TextMessage, websocket.BinaryMessage} {
		if err := c.WriteMessage(mt, []byte("hello")); err != nil {
			t.Fatal(err)
		}
		got, data, err := c.ReadMessage()
		if err != nil || got != mt || string(data) != "hello" {
			t.Fatalf("echo type %d: %d %q %v", mt, got, data, err)
		}
	}
	big := strings.Repeat("x", 1<<20)
	if err := c.WriteMessage(websocket.TextMessage, []byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := c.ReadMessage(); err != nil || len(data) != 1<<20 {
		t.Fatalf("1 MiB frame: %d bytes, %v", len(data), err)
	}

	mark := e.api.Mark()
	c = e.dialWS(t, "/examples/ws")
	_ = c.WriteMessage(websocket.TextMessage, []byte(big+"x"))
	_, _, err := c.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("frame over the limit: %v, want close 1009", err)
	}
	e.api.WaitLogSince(t, mark, timeout, "stream: websocket frame over the read limit", map[string]any{"level": "WARN"})
	if l := e.api.FindSince(mark, "", map[string]any{"level": "ERROR"}); l != nil {
		t.Fatalf("an ERROR was logged: %v", l)
	}

	e.call(t, http.MethodPost, "/examples/ws", "").want(t, 400)
	if r := e.call(t, http.MethodPost, "/examples/ws", "", "Connection", "Upgrade", "Upgrade", "websocket").want(t, 405); r.header.Get("Allow") != "" {
		t.Fatalf("Allow %q, want none", r.header.Get("Allow"))
	}
}

// Basis: keel:docs/http.md, "2c" (ServeContent: Range, HEAD); docs/guides/raw-http.md, "What a raw route does itself".
func testRawDownload(t *testing.T, e *env) {
	r := e.call(t, http.MethodGet, "/examples/download", "").want(t, 200)
	if string(r.body) != "keel layout demo file\n" || !strings.Contains(r.header.Get("Content-Disposition"), "demo.txt") ||
		r.header.Get("Content-Type") != "text/plain; charset=utf-8" || r.header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("download: %q %v", r.body, r.header)
	}
	r = e.call(t, http.MethodGet, "/examples/download", "", "Range", "bytes=0-3").want(t, 206)
	if string(r.body) != "keel" || r.header.Get("Content-Range") != "bytes 0-3/22" {
		t.Fatalf("range: %q %q", r.body, r.header.Get("Content-Range"))
	}
	e.call(t, http.MethodGet, "/examples/download", "", "Range", "bytes=100-").want(t, 416)
	r = e.call(t, http.MethodHead, "/examples/download", "").want(t, 200)
	if r.header.Get("Content-Length") != "22" || len(r.body) != 0 {
		t.Fatalf("HEAD: length %q, body %q", r.header.Get("Content-Length"), r.body)
	}
	if r := e.call(t, http.MethodPost, "/examples/download", "").want(t, 405); r.header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow %q", r.header.Get("Allow"))
	}
}

// Basis: keel:docs/http.md, "2d" (webhook); keel:docs/config.md, "Your service config (app.yaml)" (reloaded while
// running, last good config kept, an overlay created after start ignored); configs/app.yaml (no default secret).
func testWebhookAndHotReload(t *testing.T, e *env) {
	writeAppYAML(t, e.api.Dir, secretYAML(""))
	waitFor(t, "empty secret reloaded", func() bool { return postWebhook(t, e.http, "x", "{}").code == http.StatusServiceUnavailable })
	if r := postWebhook(t, e.http, "x", "{}"); strings.TrimSpace(string(r.body)) != "webhook not configured" {
		t.Fatalf("no secret: %q", r.body)
	}

	setWebhookSecret(t, e.http, e.api, "secret-one")
	mark := e.api.Mark()
	body := `{"event":"ping"}`
	postWebhook(t, e.http, "secret-one", body).want(t, 200)
	e.api.WaitLogSince(t, mark, timeout, "webhook received", map[string]any{"remote_ip": "127.0.0.1", "bytes": len(body)})
	for _, secret := range []string{"wrong", ""} {
		if r := postWebhook(t, e.http, secret, body).want(t, 401); strings.TrimSpace(string(r.body)) != "invalid signature" {
			t.Fatalf("signature %q: %q", secret, r.body)
		}
	}
	if r := e.call(t, http.MethodGet, "/examples/webhook", "").want(t, 405); r.header.Get("Allow") != "POST" {
		t.Fatalf("Allow %q", r.header.Get("Allow"))
	}
	postWebhook(t, e.http, "x", strings.Repeat("0", 1<<20+1)).want(t, 413)

	// A broken app.yaml is not applied: the last good config stays.
	mark = e.api.Mark()
	writeAppYAML(t, e.api.Dir, "custom: [\n")
	e.api.WaitLogSince(t, mark, timeout, "kconfig: reload failed, keeping last good config", nil)
	postWebhook(t, e.http, "secret-one", body).want(t, 200)

	// An overlay created after start is ignored; the main file still reloads.
	if err := os.WriteFile(filepath.Join(e.api.Dir, "configs", "app.dev.yaml"), []byte(secretYAML("from-overlay")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(e.api.Dir, "configs", "app.dev.yaml")) })
	setWebhookSecret(t, e.http, e.api, "secret-two")
	postWebhook(t, e.http, "from-overlay", body).want(t, 401)
	postWebhook(t, e.http, "secret-one", body).want(t, 401)
}

// Basis: keel:docs/http.md, "2e" (pre-routing filter); keel:docs/metadata.md, "Outside the chain";
// internal/service/webhook.go, SignedEchoHandler.
func testSignedPrefixFilter(t *testing.T, e *env) {
	setWebhookSecret(t, e.http, e.api, "secret-echo")
	echo := func(method, secret, body string, headers ...string) response {
		if secret != "" {
			headers = append(headers, "X-Signature", sign(secret, body))
		}
		return e.call(t, method, "/examples/signed/echo", body, headers...)
	}
	if r := echo(http.MethodPost, "secret-echo", "x").want(t, 200); strings.TrimSpace(string(r.body)) != "127.0.0.1" {
		t.Fatalf("echo %q", r.body)
	}
	if r := echo(http.MethodPost, "secret-echo", "x", "X-Forwarded-For", "1.2.3.4").want(t, 200); strings.TrimSpace(string(r.body)) != "1.2.3.4" {
		t.Fatalf("echo with X-Forwarded-For %q", r.body)
	}
	echo(http.MethodPost, "", "x").want(t, 401)
	echo(http.MethodGet, "secret-echo", "").want(t, 405)
	echo(http.MethodGet, "", "").want(t, 401)
	e.call(t, http.MethodGet, "/health", "").want(t, 200)
	e.call(t, http.MethodGet, "/v1/todos?limit=1", "").want(t, 200)
}

// Basis: keel:docs/http.md, "Two kinds of endpoints" (raw routes skip the middleware chain).
func testRawRoutesNotLogged(t *testing.T, e *env) {
	setWebhookSecret(t, e.http, e.api, "secret-log")
	mark := e.api.Mark()
	e.call(t, http.MethodGet, "/examples/sse", "").want(t, 200)
	e.call(t, http.MethodGet, "/examples/download", "").want(t, 200)
	postWebhook(t, e.http, "secret-log", "{}").want(t, 200)
	e.call(t, http.MethodGet, "/v1/todos?limit=1", "").want(t, 200)
	e.api.WaitLogSince(t, mark, timeout, "/v1/todos", map[string]any{"kind": "server"})
	for _, l := range e.api.LogsSince(mark) {
		if l["kind"] == "server" && strings.HasPrefix(l["msg"].(string), "/examples/") {
			t.Fatalf("a raw route has a request log: %v", l)
		}
	}
}
