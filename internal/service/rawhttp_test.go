// Example file: delete the whole file after forking

package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/middleware/metadata"
	kmd "github.com/go-kratos/kratos/v3/metadata"
)

// Raw routes bypass the middleware chain, so method restrictions are hand-written. Each of the three example
// handlers accepts only the methods it declares and answers anything else with 405 and an Allow header.
func TestRawHandlers_RejectWrongMethod(t *testing.T) {
	stream := NewStreamService(biz.NewTodoUseCase(newFakeTodoRepo(), noopPublisher{}))
	cases := []struct {
		name    string
		handler http.HandlerFunc
		allowed []string
		denied  []string
	}{
		{"sse", stream.TodosSSE(), []string{http.MethodGet}, []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead}},
		{"download", DemoDownloadHandler(), []string{http.MethodGet, http.MethodHead}, []string{http.MethodPost, http.MethodPut, http.MethodDelete}},
		// For the webhook only the rejecting half is tested: an allowed POST goes on to read the signing secret, and
		// there is no real config here, which is not worth building. The method guard runs before reading config, so
		// the rejecting path never touches it; if the guard breaks, this line fails with a nil-dereference panic, not a
		// 405 assertion. If you see a panic here, check the method guard in webhook.go first.
		{"webhook", (&WebhookHandler{}).HandleExample, nil, []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range tc.denied {
				rec := serveRecovered(t, tc.handler, m)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s %s: status = %d, want 405", tc.name, m, rec.Code)
				}
				if rec.Header().Get("Allow") == "" {
					t.Fatalf("%s %s: 405 must carry an Allow header", tc.name, m)
				}
			}
			for _, m := range tc.allowed {
				rec := serveRecovered(t, tc.handler, m)
				if rec.Code == http.StatusMethodNotAllowed {
					t.Fatalf("%s %s should pass but got 405", tc.name, m)
				}
			}
		})
	}
}

// serveRecovered turns a handler panic into a failure of this subtest. Otherwise one panic takes down the whole
// package's test process, and the later cases never run or report.
func serveRecovered(t *testing.T, h http.HandlerFunc, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("%s: handler panic: %v", method, p)
		}
	}()
	h(rec, httptest.NewRequest(method, "/x", nil))
	return rec
}

// The pre-routing filter handles only its own prefix: inside it verifies the signature with the secret, outside it
// touches not a single byte; the body read for verification must be put back, or downstream decoding reads nothing.
func TestSignatureFilter(t *testing.T) {
	const secret = "test-secret"
	const body = `{"event":"paid"}`
	cases := []struct {
		name     string
		secret   string
		path     string
		body     string
		sign     string
		want     int
		wantNext bool
	}{
		{"valid signature passes", secret, "/examples/signed/echo", body, sign(secret, body), http.StatusOK, true},
		{"wrong signature 401", secret, "/examples/signed/echo", body, sign("other", body), http.StatusUnauthorized, false},
		{"missing signature 401", secret, "/examples/signed/echo", body, "", http.StatusUnauthorized, false},
		{"no secret configured 503", "", "/examples/signed/echo", body, sign(secret, body), http.StatusServiceUnavailable, false},
		{"over the limit 413", secret, "/examples/signed/echo", strings.Repeat("x", maxWebhookBody+1), "", http.StatusRequestEntityTooLarge, false},
		{"outside the prefix not verified", secret, "/examples/webhook", body, "", http.StatusOK, true},
		{"outside the prefix passes without a secret", "", "/health", body, "", http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("downstream reading body: %v", err)
				}
				gotBody = string(b)
			})
			h := newTestWebhook(t, tc.secret).SignatureFilter("/examples/signed/")(next)

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			if tc.sign != "" {
				req.Header.Set("X-Signature", tc.sign)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if nextCalled != tc.wantNext {
				t.Fatalf("next called = %v, want %v", nextCalled, tc.wantNext)
			}
			if tc.wantNext && gotBody != tc.body {
				t.Fatalf("downstream read %q, want original body %q", gotBody, tc.body)
			}
		})
	}
}

// The echo route writes back the client IP read outside the chain; it accepts only POST.
func TestSignedEchoHandler(t *testing.T) {
	// In production api.NewHTTPServer puts the value into the context outside the chain, which a test cannot
	// reproduce; here we fill GetRemoteIP's other source and check only that the handler writes it back.
	ctx := kmd.NewServerContext(t.Context(), kmd.New(map[string][]string{metadata.KeyRemoteAddress: {"203.0.113.7"}}))

	rec := httptest.NewRecorder()
	SignedEchoHandler()(rec, httptest.NewRequest(http.MethodPost, "/examples/signed/echo", nil).WithContext(ctx))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "203.0.113.7" {
		t.Fatalf("got %d %q, want 200 203.0.113.7", rec.Code, rec.Body.String())
	}

	rec = serveRecovered(t, SignedEchoHandler(), http.MethodGet)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: got %d Allow=%q, want 405 Allow=POST", rec.Code, rec.Header().Get("Allow"))
	}
}

// newTestWebhook builds a WebhookHandler from a temporary config file; an empty secret means none configured.
func newTestWebhook(t *testing.T, secret string) *WebhookHandler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	content := "custom:\n"
	if secret != "" {
		content += "  webhook_secret: " + secret + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	bootstrap, cleanup, err := kconfig.NewDynamic[conf.Bootstrap](path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return NewWebhookHandler(bootstrap)
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
