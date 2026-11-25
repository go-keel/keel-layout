//go:build e2e

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	kerrors "github.com/go-kratos/kratos/v3/errors"
)

// response is one HTTP exchange.
type response struct {
	code   int
	header http.Header
	body   []byte
}

// json decodes the body as a JSON object.
func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not a JSON object (%v): %d %s", err, r.code, r.body)
	}
	return m
}

// want fails the test unless the status is code.
func (r response) want(t *testing.T, code int) response {
	t.Helper()
	if r.code != code {
		t.Fatalf("status %d, want %d: %s", r.code, code, r.body)
	}
	return r
}

// wantError fails the test unless the response is the structured error code + reason; it returns the message.
func (r response) wantError(t *testing.T, code int, reason string) string {
	t.Helper()
	m := r.want(t, code).json(t)
	if m["reason"] != reason {
		t.Fatalf("reason %v, want %s: %s", m["reason"], reason, r.body)
	}
	return m["message"].(string)
}

// call sends one request to base+path; headers are name, value pairs.
func call(t *testing.T, method, url, body string, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{code: resp.StatusCode, header: resp.Header, body: b}
}

func (e *env) call(t *testing.T, method, path, body string, headers ...string) response {
	t.Helper()
	return call(t, method, e.http+path, body, headers...)
}

// Example: delete after forking (through "Example end")
// create creates a Todo over HTTP and returns its id.
func (e *env) create(t *testing.T, title string) string {
	t.Helper()
	m := e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": title})).want(t, 200).json(t)
	return m["id"].(string)
}

// Example end

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// sign is the example webhook signature: hex HMAC-SHA256 of the body.
func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// wantGRPCError fails the test unless err carries code and reason once read back as a Kratos error; it returns the
// message.
func wantGRPCError(t *testing.T, err error, code int, reason string) string {
	t.Helper()
	if err == nil {
		t.Fatalf("want %d %s, got no error", code, reason)
	}
	ke := kerrors.FromError(err)
	if int(ke.Code) != code || (reason != "" && ke.Reason != reason) {
		t.Fatalf("got %d %s %q, want %d %s", ke.Code, ke.Reason, ke.Message, code, reason)
	}
	return ke.Message
}

// repeat returns s repeated n times.
func repeat(s string, n int) string { return strings.Repeat(s, n) }

// lines splits a body into its non-empty lines.
func lines(b []byte) []string {
	var out []string
	for _, l := range bytes.Split(b, []byte("\n")) {
		if s := strings.TrimRight(string(l), "\r"); s != "" {
			out = append(out, s)
		}
	}
	return out
}
