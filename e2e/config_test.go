//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-keel/keel-layout/e2e/harness"
)

// editConfig replaces old, which must occur once, with new in configs/<file> of dir.
func editConfig(t *testing.T, dir, file, old, new string) {
	t.Helper()
	p := filepath.Join(dir, "configs", file)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), old); n != 1 {
		t.Fatalf("configs/%s: %q occurs %d times, want 1", file, old, n)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

const httpAuthOff = "      auth:\n        enabled: false\n"

// grpcAddrLine anchors additions to the gRPC server's section.
const grpcAddrLine = "      addr: \"${GRPC_ADDR:0.0.0.0:9000}\"\n"

const jwtSecret = "e2e-hs256-secret-0123456789abcdef"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func claims(exp time.Duration) string {
	m := map[string]any{"sub": "e2e-user", "iat": time.Now().Unix()}
	if exp != 0 {
		m["exp"] = time.Now().Add(exp).Unix()
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// hs256 signs payload with HMAC-SHA256 under key.
func hs256(key, payload string) string {
	signing := b64([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + b64([]byte(payload))
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signing))
	return signing + "." + b64(mac.Sum(nil))
}

func rs256(t *testing.T, key *rsa.PrivateKey, payload string) string {
	t.Helper()
	signing := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64([]byte(payload))
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func bearer(tok string) []string { return []string{"Authorization", "Bearer " + tok} }

// startPrivateAPI starts an api of its own in dir (already edited) and returns its base URL.
func (e *env) startPrivateAPI(t *testing.T, dir string, extra ...string) (string, *harness.Process) {
	t.Helper()
	addr := harness.FreeAddr(t)
	return "http://" + addr, e.startAPI(t, dir, addr, extra...)
}

// Basis: keel:docs/auth.md, "Configuration" and "Anonymous endpoints"; keel:docs/http.md, "Raw routes" and
// "Liveness"; todo.proto, allow_anonymous.
func testAuthHTTP(t *testing.T, e *env) {
	dir := harness.Workdir(t)
	editConfig(t, dir, "config.yaml", httpAuthOff, "      auth: {enabled: true, algorithm: HS256, jwt_secret: \""+jwtSecret+"\"}\n")
	writeAppYAML(t, dir, secretYAML("auth-webhook"))
	base, _ := e.startPrivateAPI(t, dir)
	create := func(headers ...string) response {
		return call(t, http.MethodPost, base+"/v1/todos", mustJSON(map[string]any{"title": uniq("auth")}), headers...)
	}

	if msg := create().wantError(t, 401, "UNAUTHORIZED"); msg != "not signed in" {
		t.Fatalf("no token: %q", msg)
	}
	alg := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + b64([]byte(claims(time.Hour))) + "."
	for name, tok := range map[string]string{
		"forged":   hs256("not-the-configured-secret-0123456789", claims(time.Hour)),
		"expired":  hs256(jwtSecret, claims(-time.Hour)),
		"no exp":   hs256(jwtSecret, claims(0)),
		"alg none": alg,
	} {
		if msg := create(bearer(tok)...).wantError(t, 401, "UNAUTHORIZED"); msg != "invalid token" {
			t.Fatalf("%s token: %q", name, msg)
		}
	}
	token := hs256(jwtSecret, claims(time.Hour))
	create(bearer(token)...).want(t, 200)
	call(t, http.MethodPatch, base+"/v1/todos/1?update_mask=title", `{"title":"x"}`).wantError(t, 401, "UNAUTHORIZED")
	call(t, http.MethodDelete, base+"/v1/todos/999999999", "").wantError(t, 401, "UNAUTHORIZED")

	forged := bearer(hs256("not-the-configured-secret-0123456789", claims(time.Hour)))
	call(t, http.MethodGet, base+"/v1/todos?limit=1", "").want(t, 200)
	call(t, http.MethodGet, base+"/v1/todos?limit=1", "", forged...).want(t, 200)
	if len(sseData(call(t, http.MethodGet, base+"/v1/todos/watch?limit=1", "").want(t, 200).body)) != 1 {
		t.Fatal("anonymous WatchTodo sent no frame")
	}
	call(t, http.MethodGet, base+"/examples/sse", "", forged...).want(t, 200)
	call(t, http.MethodGet, base+"/examples/download", "", forged...).want(t, 200)
	postWebhook(t, base, "auth-webhook", "{}").want(t, 200)
	if r := call(t, http.MethodGet, base+"/health", "").want(t, 200); strings.TrimSpace(string(r.body)) != "ok" {
		t.Fatalf("/health %q", r.body)
	}
	// Without cookie_name only the header is read.
	if msg := create("Cookie", "e2e_session="+token).wantError(t, 401, "UNAUTHORIZED"); msg != "not signed in" {
		t.Fatalf("cookie without cookie_name: %q", msg)
	}
}

// Basis: keel:docs/auth.md, "Token from a cookie" (the header wins over the cookie).
func testAuthCookie(t *testing.T, e *env) {
	dir := harness.Workdir(t)
	editConfig(t, dir, "config.yaml", httpAuthOff, "      auth: {enabled: true, algorithm: HS256, jwt_secret: \""+jwtSecret+"\", cookie_name: e2e_session}\n")
	base, _ := e.startPrivateAPI(t, dir)
	token := hs256(jwtSecret, claims(time.Hour))
	body := mustJSON(map[string]any{"title": uniq("cookie")})
	call(t, http.MethodPost, base+"/v1/todos", body, "Cookie", "e2e_session="+token).want(t, 200)
	forged := hs256("not-the-configured-secret-0123456789", claims(time.Hour))
	if msg := call(t, http.MethodPost, base+"/v1/todos", body, append([]string{"Cookie", "e2e_session=" + token}, bearer(forged)...)...).wantError(t, 401, "UNAUTHORIZED"); msg != "invalid token" {
		t.Fatalf("forged header with a good cookie: %q", msg)
	}
}

// Basis: keel:docs/auth.md, "Configuration" (RS256 by default, the algorithm is fixed against algorithm confusion).
func testAuthRS256(t *testing.T, e *env) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	pubFile := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(pubFile, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := harness.Workdir(t)
	editConfig(t, dir, "config.yaml", httpAuthOff, "      auth: {enabled: true, jwt_public_key_file: \""+filepath.ToSlash(pubFile)+"\"}\n")
	base, _ := e.startPrivateAPI(t, dir)
	body := mustJSON(map[string]any{"title": uniq("rs256")})
	call(t, http.MethodPost, base+"/v1/todos", body, bearer(rs256(t, key, claims(time.Hour)))...).want(t, 200)
	call(t, http.MethodPost, base+"/v1/todos", body, bearer(hs256(jwtSecret, claims(time.Hour)))...).wantError(t, 401, "UNAUTHORIZED")
	call(t, http.MethodPost, base+"/v1/todos", body, bearer(hs256(string(pub), claims(time.Hour)))...).wantError(t, 401, "UNAUTHORIZED")
}

// Basis: keel:docs/http.md, "2e" and keel:docs/metadata.md, "How the client IP is computed" (trusted_proxies replaces
// the default, ignore_forwarded_for).
func testClientIPOutsideChain(t *testing.T, e *env) {
	for _, c := range []struct {
		clientIP, xff, want string
	}{
		{`{trusted_proxies: ["10.0.0.0/8"]}`, "1.2.3.4", "127.0.0.1"},
		{`{trusted_proxies: ["10.0.0.0/8", "127.0.0.1"]}`, "1.2.3.4, 10.1.1.1", "1.2.3.4"},
		{`{ignore_forwarded_for: true}`, "1.2.3.4", "127.0.0.1"},
	} {
		dir := harness.Workdir(t)
		editConfig(t, dir, "config.yaml", httpAuthOff, httpAuthOff+"      client_ip: "+c.clientIP+"\n")
		writeAppYAML(t, dir, secretYAML("ip"))
		base, p := e.startPrivateAPI(t, dir)
		r := call(t, http.MethodPost, base+"/examples/signed/echo", "x", "X-Signature", sign("ip", "x"), "X-Forwarded-For", c.xff).want(t, 200)
		if got := strings.TrimSpace(string(r.body)); got != c.want {
			t.Fatalf("client_ip %s, X-Forwarded-For %q: %q, want %q", c.clientIP, c.xff, got, c.want)
		}
		p.Stop(t)
	}
}

// Basis: keel:docs/config.md, "Environment overrides (KEEL_ prefix)" (placeholders are filled by KEEL_ variables; a
// variable never sets a field by name) and "Validation" (unknown keys are ignored).
func testConfigPlaceholderAndEnv(t *testing.T, e *env) {
	// The round's processes run on ports and servers that came in through placeholders; here a variable that names a
	// field changes nothing.
	base, p := e.startPrivateAPI(t, harness.Workdir(t), "KEEL_LOGGER_LEVEL=warn")
	mark := p.Mark()
	call(t, http.MethodGet, base+"/v1/todos?limit=1", "").want(t, 200)
	p.WaitLogSince(t, mark, timeout, "/v1/todos", map[string]any{"kind": "server", "level": "INFO"})
	p.Stop(t)

	dir := harness.Workdir(t)
	editConfig(t, dir, "config.yaml", "  format: ", "  formt: ")
	_, p = e.startPrivateAPI(t, dir)
	if len(p.LogsSince(0)) != 0 || !strings.Contains(p.Output(), "level=INFO") {
		t.Fatalf("with the format key misspelt the log should stay text:\n%s", p.Tail(5))
	}
}

// Basis: keel:docs/config.md, "Files" (a dev overlay logs a warning) and "Declaring production (KEEL_ENV)".
func testConfigDevOverlayAndKeelEnv(t *testing.T, e *env) {
	overlay := func(dir, file string) {
		if err := os.WriteFile(filepath.Join(dir, "configs", file), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := harness.Workdir(t)
	overlay(dir, "config.dev.yaml")
	_, p := e.startPrivateAPI(t, dir)
	if !strings.Contains(p.Output(), "kconfig: dev overlay loaded") {
		t.Fatalf("no dev overlay warning:\n%s", p.Tail(10))
	}
	p.Stop(t)

	for _, file := range []string{"app.dev.yaml", "config.dev.yaml"} {
		dir := harness.Workdir(t)
		overlay(dir, file)
		out := e.mustFail(t, "api", dir, "kconfig.dev-overlay-in-prod", "KEEL_ENV=prod")
		if !strings.Contains(out, "configs/"+file) {
			t.Fatalf("KEEL_ENV=prod with %s: the message does not name it:\n%s", file, out)
		}
	}
	for _, v := range []string{"production", "PROD"} {
		e.mustFail(t, "api", harness.Workdir(t), "kconfig.env-invalid", "KEEL_ENV="+v)
	}
	_, p = e.startPrivateAPI(t, harness.Workdir(t), "KEEL_ENV=dev")
	p.Stop(t)
}

// Basis: docs/guides/config.md, the table of what reloads (the framework config does not, app.yaml does).
func testConfigFrameworkNotReloaded(t *testing.T, e *env) {
	dir := harness.Workdir(t)
	base, p := e.startPrivateAPI(t, dir)
	editConfig(t, dir, "config.yaml", "  level: info\n", "  level: warn\n")
	setWebhookSecret(t, base, p, "after-edit") // app.yaml reloaded after the config.yaml edit
	mark := p.Mark()
	call(t, http.MethodGet, base+"/v1/todos?limit=1", "").want(t, 200)
	p.WaitLogSince(t, mark, timeout, "/v1/todos", map[string]any{"kind": "server", "level": "INFO"})
}
