//go:build e2e

// Shared file: carried verbatim; never localized outside its source repository.
// Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.

// Package harness runs the repository's production binaries for end-to-end tests (build tag e2e, run by make e2e):
// it builds each cmd/<name> with go build as a release is built, starts it as a child process in a temporary
// directory holding only the committed configs/config.yaml and configs/app.yaml, points it at the test servers
// through KEEL_ variables, collects its JSON log, and stops it the way a deployment does. docs/guides/testing.md,
// "End-to-end tests", describes the whole layer.
package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/go-keel/keel-layout/internal/testdb"
)

// WaitTimeout is how long WaitLog and the readiness checks wait by default.
const WaitTimeout = 10 * time.Second

// StopTimeout bounds a graceful stop: app.stop_timeout plus the NATS connection cleanup budget, with room to spare.
const StopTimeout = 40 * time.Second

// Vars are the variables pointing at the test servers; a missing one fails the test.
var Vars = []string{"TEST_MYSQL_DSN", "TEST_POSTGRES_DSN", "TEST_REDIS_ADDR", "TEST_NATS_URL"}

// Config returns the string at key (dotted, as in config.Value) in the committed configs/<name>, the file the processes
// Start runs read: a test asserts the repository's own values (application name, cookie name, brand) without spelling
// them out, so it keeps passing after a fork replaces them.
func Config(t testing.TB, name, key string) string {
	t.Helper()
	c := config.New(config.WithSource(file.NewSource(filepath.Join(testdb.Root(t), "configs", name))))
	defer func() { _ = c.Close() }()
	if err := c.Load(); err != nil {
		t.Fatalf("load configs/%s: %v", name, err)
	}
	v, err := c.Value(key).String()
	if err != nil {
		t.Fatalf("configs/%s: %s: %v", name, key, err)
	}
	return v
}

// Env returns the value of one of Vars, failing the test when it is unset.
func Env(t testing.TB, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set: make e2e needs all of %s (docs/guides/testing.md)", name, strings.Join(Vars, ", "))
	}
	return v
}

var (
	buildMu sync.Mutex
	binDir  string
	built   = map[string]bool{}
)

// BuildTags are the build tags of the release build (the Makefile's build target), set before the first Build when
// that build uses any; for example keel-admin sets "embed_ui". Empty means no tags.
var BuildTags string

// Build compiles ./cmd/<name> for each name, as the release build does (CGO_ENABLED=0, BuildTags), once per test
// binary. Start runs what it built.
func Build(t testing.TB, names ...string) {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if binDir == "" {
		dir, err := os.MkdirTemp("", "e2e-bin-")
		if err != nil {
			t.Fatal(err)
		}
		binDir = dir // removed by the OS temp cleanup; kept across tests of one run
	}
	root := testdb.Root(t)
	for _, name := range names {
		if built[name] {
			continue
		}
		args := []string{"build", "-trimpath"}
		if BuildTags != "" {
			args = append(args, "-tags", BuildTags)
		}
		cmd := exec.Command("go", append(args, "-o", bin(name), "./cmd/"+name)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build ./cmd/%s: %v\n%s", name, err, out)
		}
		built[name] = true
	}
}

func bin(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(binDir, name)
}

// Workdir returns a new temporary directory holding a copy of the committed configs/config.yaml and configs/app.yaml,
// and nothing else: a developer's configs/*.dev.yaml never reaches a test. A test may edit the copies before Start.
func Workdir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := testdb.Root(t)
	for _, f := range []string{"config.yaml", "app.yaml"} {
		b, err := os.ReadFile(filepath.Join(root, "configs", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "configs", f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// FreeAddr returns 127.0.0.1:<port> for a port free at the time of the call.
func FreeAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// ServerEnv returns the KEEL_ variables that point a process at the Redis and NATS test servers, switch its log to
// JSON and send its telemetry to a sink. They fill the ${REDIS_ADDR}, ${NATS_URL}, ${LOGGER_FORMAT} and
// ${OTEL_ENDPOINT} placeholders of configs/config.yaml.
func ServerEnv(t testing.TB) []string {
	t.Helper()
	return []string{
		"KEEL_LOGGER_FORMAT=json",
		"KEEL_REDIS_ADDR=" + Env(t, "TEST_REDIS_ADDR"),
		"KEEL_NATS_URL=" + Env(t, "TEST_NATS_URL"),
		"KEEL_OTEL_ENDPOINT=" + otelSink(t),
	}
}

var (
	sinkOnce sync.Once
	sinkAddr string
	sinkErr  error
)

// otelSink returns the address of a gRPC server, started once per test binary, that accepts every call and answers it
// with an empty message: an OTLP collector that keeps nothing. Telemetry stays on as configured, so logs carry trace
// IDs; an absent collector would instead hold each process's shutdown for the exporter's timeout and fill the log
// with export errors.
func otelSink(t testing.TB) string {
	t.Helper()
	sinkOnce.Do(func() {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			sinkErr = err
			return
		}
		sinkAddr = l.Addr().String()
		accept := func(_ any, stream grpc.ServerStream) error {
			var m emptypb.Empty // unknown fields are kept, so any request decodes
			if err := stream.RecvMsg(&m); err != nil {
				return err
			}
			return stream.SendMsg(&emptypb.Empty{}) // decodes as any empty response
		}
		go func() { _ = grpc.NewServer(grpc.UnknownServiceHandler(accept)).Serve(l) }()
	})
	if sinkErr != nil {
		t.Fatalf("start the telemetry sink: %v", sinkErr)
	}
	return sinkAddr
}

// DBEnv returns the KEEL_ variables for database.<name>: they fill the ${DB_<NAME>_DRIVER} and ${DB_<NAME>_SOURCE}
// placeholders of configs/config.yaml.
func DBEnv(name, driver, source string) []string {
	n := strings.ToUpper(name)
	return []string{"KEEL_DB_" + n + "_DRIVER=" + driver, "KEEL_DB_" + n + "_SOURCE=" + source}
}

// FlushRedis empties database 0 of the Redis test server: every round starts from an empty Redis.
func FlushRedis(t testing.TB) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: Env(t, "TEST_REDIS_ADDR")})
	defer func() { _ = rdb.Close() }()
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush Redis database 0: %v", err)
	}
}

// Process is one running binary.
type Process struct {
	Name string
	Dir  string // working directory, holding configs/

	cmd    *exec.Cmd
	done   chan struct{}
	code   int // exit code, valid after done is closed
	mu     sync.Mutex
	lines  []string
	notify chan struct{} // closed and replaced on every new line
}

// Start runs the binary Build built for name in dir with env added to the environment (KEEL_ variables already set
// in the test's environment are dropped first), and stops it when the test ends. Wait for readiness with WaitHTTP,
// WaitDial or WaitLog.
func Start(t testing.TB, name, dir string, env ...string) *Process {
	t.Helper()
	buildMu.Lock()
	ok := built[name]
	buildMu.Unlock()
	if !ok {
		t.Fatalf("harness.Start(%q): call harness.Build(t, %q) first", name, name)
	}
	p := &Process{Name: name, Dir: dir, done: make(chan struct{}), notify: make(chan struct{})}
	p.cmd = exec.Command(bin(name))
	p.cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KEEL_") {
			p.cmd.Env = append(p.cmd.Env, kv)
		}
	}
	p.cmd.Env = append(p.cmd.Env, env...)
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stderr = p.cmd.Stdout
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		p.collect(out)
		err := p.cmd.Wait()
		p.code = 0
		if err != nil {
			p.code = -1
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				p.code = exit.ExitCode()
			}
		}
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.Stop(t)
		}
	})
	return p
}

func (p *Process) collect(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		p.mu.Lock()
		p.lines = append(p.lines, sc.Text())
		close(p.notify)
		p.notify = make(chan struct{})
		p.mu.Unlock()
	}
}

// Stop ends the process as a deployment does and checks that it went cleanly: on macOS and Linux it sends SIGTERM and
// fails the test unless the process exits with 0 within StopTimeout. Windows has no SIGTERM to send, so there it kills
// the process and checks nothing; a clean stop is the framework's behaviour, covered on macOS.
func (p *Process) Stop(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		_ = p.cmd.Process.Kill()
		<-p.done
		return
	}
	select {
	case <-p.done:
		t.Fatalf("%s had already exited with %d before Stop\n%s", p.Name, p.code, p.Tail(50))
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	code, ok := p.Wait(StopTimeout)
	if !ok {
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("%s did not exit within %s of SIGTERM\n%s", p.Name, StopTimeout, p.Tail(50))
	}
	if code != 0 {
		t.Fatalf("%s exited with %d after SIGTERM, want 0\n%s", p.Name, code, p.Tail(50))
	}
}

// Signal sends sig to the process.
func (p *Process) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// Wait waits up to d for the process to exit and returns its exit code; ok is false when it is still running.
func (p *Process) Wait(d time.Duration) (code int, ok bool) {
	select {
	case <-p.done:
		return p.code, true
	case <-time.After(d):
		return 0, false
	}
}

// Exited reports whether the process has exited.
func (p *Process) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// WaitHTTP waits until GET url answers 200, failing the test after WaitTimeout or when the process exits.
func (p *Process) WaitHTTP(t testing.TB, url string) {
	t.Helper()
	p.waitReady(t, "GET "+url, func() error {
		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
}

// WaitDial waits until addr accepts a TCP connection, failing the test after WaitTimeout or when the process exits.
func (p *Process) WaitDial(t testing.TB, addr string) {
	t.Helper()
	p.waitReady(t, "dial "+addr, func() error {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
		}
		return err
	})
}

func (p *Process) waitReady(t testing.TB, what string, try func() error) {
	t.Helper()
	deadline := time.Now().Add(WaitTimeout)
	for {
		err := try()
		if err == nil {
			return
		}
		if p.Exited() {
			t.Fatalf("%s exited with %d before %s succeeded\n%s", p.Name, p.code, what, p.Tail(50))
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s did not succeed within %s: %v\n%s", p.Name, what, WaitTimeout, err, p.Tail(50))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Line is one JSON log line; numbers keep their text (json.Number), so fmt.Sprint of a field prints what was logged.
type Line map[string]any

// Match reports whether the line has msg (any msg when it is empty) and every field in fields; a field matches when fmt.Sprint of both sides
// is equal, so 42, "42" and json.Number("42") all match; a nil value only requires the field to be present.
func (l Line) Match(msg string, fields map[string]any) bool {
	if msg != "" && l["msg"] != msg {
		return false
	}
	for k, want := range fields {
		got, ok := l[k]
		if !ok || want != nil && fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

// Mark returns the current end of the log; pass it to WaitLogSince or LogsSince to look only at what follows.
func (p *Process) Mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.lines)
}

// WaitLog waits up to WaitTimeout for a JSON log line, anywhere in the log, with msg and fields (see Line.Match). On
// timeout it fails the test and prints the last 50 lines of the log.
func (p *Process) WaitLog(t testing.TB, msg string, fields map[string]any) Line {
	t.Helper()
	return p.WaitLogSince(t, 0, WaitTimeout, msg, fields)
}

// WaitLogSince is WaitLog over the lines after mark (from Mark), waiting up to d.
func (p *Process) WaitLogSince(t testing.TB, mark int, d time.Duration, msg string, fields map[string]any) Line {
	t.Helper()
	deadline := time.After(d)
	for {
		p.mu.Lock()
		lines, notify := p.lines[mark:], p.notify
		p.mu.Unlock()
		for _, s := range lines {
			if l, ok := parse(s); ok && l.Match(msg, fields) {
				return l
			}
		}
		mark += len(lines)
		select {
		case <-notify:
		case <-p.done:
			// The process may have written the line just before exiting; look once more.
			if l := p.FindSince(mark, msg, fields); l != nil {
				return l
			}
			t.Fatalf("%s exited with %d before logging %q %v\n%s", p.Name, p.code, msg, fields, p.Tail(50))
		case <-deadline:
			t.Fatalf("%s did not log %q %v within %s\n%s", p.Name, msg, fields, d, p.Tail(50))
		}
	}
}

// FindSince returns the first JSON log line after mark with msg and fields, or nil.
func (p *Process) FindSince(mark int, msg string, fields map[string]any) Line {
	for _, l := range p.LogsSince(mark) {
		if l.Match(msg, fields) {
			return l
		}
	}
	return nil
}

// LogsSince returns the JSON log lines after mark; lines that are not JSON (written before the logger is set up) are
// left out.
func (p *Process) LogsSince(mark int) []Line {
	p.mu.Lock()
	raw := append([]string(nil), p.lines[mark:]...)
	p.mu.Unlock()
	var out []Line
	for _, s := range raw {
		if l, ok := parse(s); ok {
			out = append(out, l)
		}
	}
	return out
}

// Output returns everything the process has written so far, one line per log line.
func (p *Process) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

// Tail returns the last n lines the process wrote.
func (p *Process) Tail(n int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := p.lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return fmt.Sprintf("--- last %d lines of %s ---\n%s", len(lines), p.Name, strings.Join(lines, "\n"))
}

func parse(s string) (Line, bool) {
	if !strings.HasPrefix(s, "{") {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	var l Line
	if d.Decode(&l) != nil {
		return nil, false
	}
	return l, true
}
