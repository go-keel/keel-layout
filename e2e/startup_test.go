//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/go-keel/keel-layout/e2e/harness"
)

// mustFail starts name in dir and expects it to exit non-zero within the default wait with keel/<rule> in its output
// ("" for no rule); it returns the output.
func (e *env) mustFail(t *testing.T, name, dir, rule string, extra ...string) string {
	t.Helper()
	p := harness.Start(t, name, dir, append(e.processEnv(t), extra...)...)
	code, ok := p.Wait(timeout)
	if !ok {
		t.Fatalf("%s still running after %s, want a failed start (%s)\n%s", name, timeout, rule, p.Tail(20))
	}
	out := p.Output()
	if code == 0 || (rule != "" && !strings.Contains(out, "keel/"+rule)) {
		t.Fatalf("%s exited with %d, want non-zero with keel/%s\n%s", name, code, rule, p.Tail(20))
	}
	return out
}

// Basis: keel:docs/config.md, "Validation"; keel:docs/errors.md, the rule of each case; keel:docs/auth.md,
// "Configuration"; keel:docs/metadata.md, "How the client IP is computed"; keel:docs/observability.md, "Config" and
// "Sampling"; keel:docs/tasks.md, "Configuration".
func testStartupErrors(t *testing.T, e *env) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := filepath.Join(t.TempDir(), "pkcs1.pem")
	if err := os.WriteFile(pkcs1, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}), 0o644); err != nil {
		t.Fatal(err)
	}
	auth := func(v string) [3]string { return [3]string{"config.yaml", httpAuthOff, "      auth: " + v + "\n"} }
	clientIP := func(v string) [3]string {
		return [3]string{"config.yaml", httpAuthOff, httpAuthOff + "      client_ip: " + v + "\n"}
	}
	cases := []struct {
		name  string
		cmd   string
		edits [][3]string
		env   []string
		rule  string
		also  []string // more text the output must hold
	}{
		{"two invalid keys", "api", [][3]string{
			{"config.yaml", "  level: info\n", "  level: INFO\n"},
			{"config.yaml", "  node_id: 0 ", "  node_id: 2000 "},
		}, nil, "kconfig.invalid", []string{"logger.level", "app.node_id"}},
		{"not a number", "api", [][3]string{{"config.yaml", "  node_id: 0 ", "  node_id: abc "}}, nil, "kconfig.scan", nil},
		{"HS256 without a secret", "api", [][3]string{auth("{enabled: true, algorithm: HS256}")}, nil, "kconfig.invalid", nil},
		{"unsupported algorithm", "api", [][3]string{auth(`{enabled: true, algorithm: ES256, jwt_secret: "x"}`)}, nil, "kconfig.invalid", nil},
		{"missing key file", "api", [][3]string{auth(`{enabled: true, jwt_public_key_file: "/nonexistent/e2e.pem"}`)}, nil, "jwt.key-file-unreadable", []string{"/nonexistent/e2e.pem"}},
		{"trusted proxy not an address", "api", [][3]string{clientIP(`{trusted_proxies: ["abc"]}`)}, nil, "kconfig.invalid", nil},
		{"trusted proxies with forwarding ignored", "api", [][3]string{clientIP(`{trusted_proxies: ["10.0.0.0/8"], ignore_forwarded_for: true}`)}, nil, "kconfig.invalid", nil},
		{"otel without an endpoint", "api", nil, []string{"KEEL_OTEL_ENDPOINT="}, "kconfig.invalid", nil},
		{"sample ratio over 1", "api", [][3]string{{"config.yaml", "otel:\n", "otel:\n  trace_sample_ratio: 1.5\n"}}, nil, "kconfig.invalid", nil},
		{"negative stop timeout", "api", [][3]string{{"config.yaml", "  stop_timeout: 20s ", "  stop_timeout: -1s "}}, nil, "kconfig.invalid", nil},
		{"queue weight 0", "worker", [][3]string{{"config.yaml", "      default: 6\n", "      default: 0\n"}}, nil, "kconfig.invalid", nil},
		{"empty app.domain", "api", [][3]string{{"config.yaml", "  domain: keel\n", "  domain: \"\"\n"}}, nil, "knats.domain-missing", nil},
		{"empty NATS url", "api", nil, []string{"KEEL_NATS_URL="}, "kconfig.invalid", nil},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			dir := harness.Workdir(t)
			for _, ed := range c.edits {
				editConfig(t, dir, ed[0], ed[1], ed[2])
			}
			out := e.mustFail(t, c.cmd, dir, c.rule, c.env...)
			for _, s := range c.also {
				if !strings.Contains(out, s) {
					t.Fatalf("output does not name %q:\n%s", s, out)
				}
			}
		})
	}

	t.Run("no_configs_directory", func(t *testing.T) {
		e.mustFail(t, "api", t.TempDir(), "kconfig.load")
	})
	t.Run("PKCS1_public_key_accepted", func(t *testing.T) {
		dir := harness.Workdir(t)
		editConfig(t, dir, "config.yaml", httpAuthOff, "      auth: {enabled: true, jwt_public_key_file: \""+filepath.ToSlash(pkcs1)+"\"}\n")
		_, p := e.startPrivateAPI(t, dir)
		p.Stop(t)
	})
}

// Basis: keel:docs/database.md, "Dialects" and the opening section (the connection is checked at start);
// keel:docs/errors.md, kdb.source-invalid.
func testDatabaseStartupErrors(t *testing.T, e *env) {
	e.mustFail(t, "api", harness.Workdir(t), "kconfig.invalid", "KEEL_DB_DEFAULT_DRIVER=sqlite")
	e.mustFail(t, "api", harness.Workdir(t), "kdb.source-invalid", "KEEL_DB_DEFAULT_SOURCE=not a dsn")
	unreachable := map[string]string{
		"mysql":    "root:x@tcp(127.0.0.1:1)/x?parseTime=true",
		"postgres": "postgres://x:x@127.0.0.1:1/x?sslmode=disable",
	}[e.round.business]
	e.mustFail(t, "api", harness.Workdir(t), "", "KEEL_DB_DEFAULT_SOURCE="+unreachable)
}

// Basis: keel:docs/messaging.md, "Model" (no waiting for NATS at start) and "Who creates a stream decides its config";
// keel:docs/redis.md, the opening section; keel:docs/tasks.md, "Redis eviction policy".
func testServerStartupErrors(t *testing.T, e *env) {
	out := e.mustFail(t, "api", harness.Workdir(t), "", "KEEL_NATS_URL=nats://127.0.0.1:1")
	if !strings.Contains(out, "no servers available for connection") {
		t.Fatalf("NATS unreachable:\n%s", out)
	}
	e.mustFail(t, "worker", harness.Workdir(t), "", "KEEL_REDIS_ADDR=127.0.0.1:1")

	// A stream that already exists under this service's name but without its subjects is not overwritten.
	ctx := context.Background()
	other := "e2eother"
	if _, err := e.js.CreateStream(ctx, jetstream.StreamConfig{Name: other + "_todo", Subjects: []string{other + ".other.>"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.js.DeleteStream(ctx, other+"_todo") })
	dir := harness.Workdir(t)
	editConfig(t, dir, "config.yaml", "  domain: keel\n", "  domain: "+other+"\n")
	e.mustFail(t, "api", dir, "knats.stream-subjects")

	policy, err := e.rdb.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rdb.ConfigSet(ctx, "maxmemory-policy", "allkeys-lru").Err(); err != nil {
		t.Fatal(err)
	}
	defer e.rdb.ConfigSet(ctx, "maxmemory-policy", policy["maxmemory-policy"])
	e.mustFail(t, "worker", harness.Workdir(t), "kasynq.eviction-policy")
}

// Basis: keel:docs/http.md, "Assembling a server" (one signal stops everything; a second exits at once with 130).
func testShutdownSecondSignal(t *testing.T, e *env) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows")
	}
	addr := harness.FreeAddr(t)
	p := e.startAPI(t, harness.Workdir(t), addr)
	// A request whose body never completes keeps the server waiting for its stop budget.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("POST /v1/todos HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let the server read the headers
	_ = p.Signal(syscall.SIGTERM)
	if _, ok := p.Wait(time.Second); ok {
		t.Fatalf("the api exited at once with a request in flight\n%s", p.Tail(10))
	}
	start := time.Now()
	_ = p.Signal(syscall.SIGTERM)
	code, ok := p.Wait(timeout)
	if !ok || code != 130 || time.Since(start) > 3*time.Second {
		t.Fatalf("after a second signal: exited %v with %d in %s, want 130 at once", ok, code, time.Since(start))
	}
}
