//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/go-keel/keel"
	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/e2e/harness"
	"github.com/go-keel/keel/conf"
	"github.com/go-keel/keel/core/kconfig"
)

// Basis: keel:docs/http.md, "Liveness: every HTTP server has /health" and "Assembling a server" (routes are listed at
// start; the plain-text port also speaks h2c).
func testHealthAndRouteLog(t *testing.T, e *env) {
	if r := e.call(t, http.MethodGet, "/health", "").want(t, 200); strings.TrimSpace(string(r.body)) != "ok" {
		t.Fatalf("/health %q", r.body)
	}
	e.call(t, http.MethodHead, "/health", "").want(t, 200)
	e.call(t, http.MethodPost, "/health", "").want(t, 405)

	mark := e.api.Mark()
	for range 3 {
		e.call(t, http.MethodGet, "/health", "").want(t, 200)
	}
	e.call(t, http.MethodGet, "/v1/todos?limit=1", "").want(t, 200)
	e.api.WaitLogSince(t, mark, timeout, "/v1/todos", map[string]any{"kind": "server"})
	for _, l := range e.api.LogsSince(mark) {
		if strings.Contains(l["msg"].(string), "/health") {
			t.Fatalf("/health was logged: %v", l)
		}
	}

	var routes []string
	for _, l := range e.api.LogsSince(0) {
		if l.Match("http route registered", nil) {
			routes = append(routes, l["method"].(string)+" "+l["path"].(string))
		}
	}
	if len(routes) != 8 || strings.Contains(strings.Join(routes, ","), "/examples/") {
		t.Fatalf("routes listed at start: %v, want the 8 contract routes", routes)
	}

	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	c := &http.Client{Transport: &http.Transport{Protocols: &p}}
	resp, err := c.Get(e.http + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("h2c: answered over %s", resp.Proto)
	}
}

// Basis: keel:docs/middleware.md, "Logging" (one line per request, 4xx at WARN, no body or headers).
func testRequestLog(t *testing.T, e *env) {
	id := e.create(t, uniq("request log"))
	for path, level := range map[string]string{"/v1/todos/" + id: "INFO", "/v1/todos/999999999": "WARN", "/v1/todos/0": "WARN"} {
		mark := e.api.Mark()
		e.call(t, http.MethodGet, path, "", "X-E2E-Header", "header-value")
		l := e.api.WaitLogSince(t, mark, timeout, "", map[string]any{"kind": "server", "component": "http", "operation": path, "level": level})
		for _, k := range []string{"latency", "response_code"} {
			if _, ok := l[k]; !ok {
				t.Fatalf("request log without %s: %v", k, l)
			}
		}
		if s := mustJSON(l); strings.Contains(s, "header-value") {
			t.Fatalf("the request log carries a header: %s", s)
		}
	}
}

// Basis: keel:docs/middleware.md, "Logging" (an SSE stream asked for with Accept and a WebSocket log an open record
// and a final one); keel:docs/grpc.md, "Streams run the chain once per stream".
func testStreamRequestLog(t *testing.T, e *env) {
	e.ensure(t, 1)
	op := "/v1/todos/watch"
	opens := func(mark int, p *harness.Process, op string) int {
		return countLines(p.LogsSince(mark), "", map[string]any{"operation": op, "phase": "open"})
	}
	mark := e.api.Mark()
	e.call(t, http.MethodGet, "/v1/todos/watch?limit=1", "", "Accept", "text/event-stream").want(t, 200)
	e.api.WaitLogSince(t, mark, timeout, "", map[string]any{"kind": "server", "operation": op, "response_code": 200})
	if n := opens(mark, e.api, op); n != 1 {
		t.Fatalf("SSE with Accept: %d open records, want 1", n)
	}
	mark = e.api.Mark()
	e.call(t, http.MethodGet, "/v1/todos/watch?limit=1", "").want(t, 200)
	e.api.WaitLogSince(t, mark, timeout, "", map[string]any{"kind": "server", "operation": op, "response_code": 200})
	if n := opens(mark, e.api, op); n != 0 {
		t.Fatalf("SSE without Accept: %d open records, want 0", n)
	}

	mark = e.api.Mark()
	c := e.dialWS(t, "/v1/todos/lookup")
	e.api.WaitLogSince(t, mark, timeout, "", map[string]any{"operation": "/v1/todos/lookup", "phase": "open"})
	_ = c.Close()
	e.api.WaitLogSince(t, mark, timeout, "", map[string]any{"kind": "server", "operation": "/v1/todos/lookup", "latency": nil})

	lookup := "/api.keel_layout.v1.TodoService/StreamTodoLookup"

	mark = e.svc.Mark()
	s, err := e.todo.StreamTodoLookup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = s.Send(&v1.GetTodoRequest{Id: 999999999})
	}
	_ = s.CloseSend()
	_, _ = s.Recv()
	e.svc.WaitLogSince(t, mark, timeout, "", map[string]any{"component": "grpc", "operation": lookup})
	ls := e.svc.LogsSince(mark)
	if countLines(ls, "", map[string]any{"operation": lookup}) != 1 || opens(mark, e.svc, lookup) != 0 {
		t.Fatalf("a gRPC stream logs one record and no open record: %v", ls)
	}
}

// Basis: keel:docs/observability.md, "Tracing" (an event and its task continue the request's trace).
func testTraceFollowsEventAndTask(t *testing.T, e *env) {
	traceID := randomHex(t, 16)
	r := e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": uniq("traced")}),
		"traceparent", "00-"+traceID+"-"+randomHex(t, 8)+"-01").want(t, 200)
	id := r.json(t)["id"].(string)
	e.api.WaitLog(t, "/v1/todos", map[string]any{"kind": "server", "trace_id": traceID})
	for _, msg := range []string{"todo_created event handled", "sending todo reminder"} {
		e.worker.WaitLog(t, msg, map[string]any{"todo_id": id, "trace_id": traceID})
	}
}

func randomHex(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// Basis: keel:docs/grpc.md (authentication comes from the server's own auth section; health checks and reflection on
// an authenticated server need a token); keel:docs/client.md, "Creating a client" (only for servers without auth).
func testAuthGRPC(t *testing.T, e *env) {
	dir := harness.Workdir(t)
	addr := harness.FreeAddr(t)
	editConfig(t, dir, "config.yaml", grpcAddrLine, "      addr: \""+addr+"\"\n      auth: {enabled: true, algorithm: HS256, jwt_secret: \""+jwtSecret+"\"}\n")
	p := harness.Start(t, "svc", dir, e.processEnv(t)...)
	p.WaitDial(t, addr)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	client := v1.NewTodoServiceClient(conn)
	anon := context.Background()
	authed := metadata.AppendToOutgoingContext(anon, "authorization", "Bearer "+hs256(jwtSecret, claims(time.Hour)))

	_, err = client.CreateTodo(anon, &v1.CreateTodoRequest{Title: "x"})
	wantGRPCError(t, err, 401, "UNAUTHORIZED")
	if _, err := client.CreateTodo(authed, &v1.CreateTodoRequest{Title: uniq("grpc auth")}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTodo(anon, &v1.ListTodoRequest{}); err != nil {
		t.Fatalf("anonymous ListTodo: %v", err)
	}

	health := grpc_health_v1.NewHealthClient(conn)
	if _, err := health.Check(anon, &grpc_health_v1.HealthCheckRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("health without a token: %v, want Unauthenticated", err)
	}
	if resp, err := health.Check(authed, &grpc_health_v1.HealthCheckRequest{}); err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health with a token: %v %v", resp, err)
	}
	if services := listServices(t, authed, conn); len(services) == 0 {
		t.Fatal("reflection with a token lists nothing")
	}

	cfg := &kconfig.Config{Config: &conf.Config{Client: map[string]*conf.Client{"todo-svc": {Endpoint: addr}}}}
	c, cleanup, err := keel.NewServiceClient(keel.NewServiceClientFactory(keel.NewConfig(cfg)), "todo-svc", v1.NewTodoServiceClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, err = c.ListTodo(anon, &v1.ListTodoRequest{})
	wantGRPCError(t, err, 503, "")
}
