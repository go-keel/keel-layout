//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/go-keel/keel"
	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel/conf"
	"github.com/go-keel/keel/core/kconfig"
	kmetadata "github.com/go-keel/keel/middleware/metadata"
)

// Basis: keel:docs/grpc.md, "Errors and tracing"; api/keel_layout/v1/todo.proto, the unary RPCs.
func testGRPCCRUD(t *testing.T, e *env) {
	ctx := context.Background()
	title := uniq("grpc")
	created, err := e.todo.CreateTodo(ctx, &v1.CreateTodoRequest{Title: title, Content: "g1"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetId()
	if e.count(t, "id = ? AND title = ? AND content = ?", id, title, "g1") != 1 {
		t.Fatal("created row not in the database")
	}
	if got, err := e.todo.GetTodo(ctx, &v1.GetTodoRequest{Id: id}); err != nil || got.GetTitle() != title {
		t.Fatalf("GetTodo: %v %v", got, err)
	}
	list, err := e.todo.ListTodo(ctx, &v1.ListTodoRequest{Completed: 2, Limit: 3})
	if err != nil || len(list.GetTodos()) != 3 || list.GetTotal() != e.count(t, "completed = ?", false) {
		t.Fatalf("ListTodo completed=2 limit=3: %d rows, total %d (database %d), %v", len(list.GetTodos()), list.GetTotal(), e.count(t, "completed = ?", false), err)
	}
	v2 := uniq("grpc v2")
	updated, err := e.todo.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: id, Title: v2, Content: "ignored"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	})
	if err != nil || updated.GetTitle() != v2 || updated.GetContent() != "g1" {
		t.Fatalf("UpdateTodo: %v %v", updated, err)
	}
	if _, err := e.todo.DeleteTodo(ctx, &v1.DeleteTodoRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	_, err = e.todo.GetTodo(ctx, &v1.GetTodoRequest{Id: id})
	wantGRPCError(t, err, 404, "TODO_NOT_FOUND")
	_, err = e.todo.CreateTodo(ctx, &v1.CreateTodoRequest{})
	wantGRPCError(t, err, 400, "VALIDATOR")
}

// Basis: keel:docs/metadata.md, Accept-Language; internal/i18n/active.zh-CN.toml.
func testGRPCErrorsAndLocale(t *testing.T, e *env) {
	ctx := metadata.AppendToOutgoingContext(context.Background(), "accept-language", "zh-CN")
	_, err := e.todo.GetTodo(ctx, &v1.GetTodoRequest{Id: 999999999})
	if msg := wantGRPCError(t, err, 404, "TODO_NOT_FOUND"); msg != "待办事项未找到。" {
		t.Fatalf("message %q, want the zh-CN translation", msg)
	}
}

// Basis: keel:docs/client.md, "Creating a client"; keel:docs/metadata.md (x-keel-accept-language travels down).
func testGRPCKeelClient(t *testing.T, e *env) {
	cfg := &kconfig.Config{Config: &conf.Config{Client: map[string]*conf.Client{"todo-svc": {Endpoint: e.grpcAddr}}}}
	if _, _, err := keel.NewServiceClient(cfg, "no-such-svc", v1.NewTodoServiceClient, nil); err == nil || !strings.Contains(err.Error(), "client.config-missing") {
		t.Fatalf("unknown client name: %v, want keel/client.config-missing", err)
	}
	c, cleanup, err := keel.NewServiceClient(cfg, "todo-svc", v1.NewTodoServiceClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, err := kmetadata.Set(context.Background(), kmetadata.KeyAcceptLanguage, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}

	created, err := c.CreateTodo(ctx, &v1.CreateTodoRequest{Title: uniq("keel client")})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetId()
	if _, err := c.GetTodo(ctx, &v1.GetTodoRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTodo(ctx, &v1.ListTodoRequest{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateTodo(ctx, &v1.UpdateTodoRequest{Todo: &v1.Todo{Id: id, Completed: true}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}}}); err != nil {
		t.Fatal(err)
	}
	if body, err := c.ExportTodo(ctx, &v1.ExportTodoRequest{Limit: 3}); err != nil || body.GetContentType() != "text/csv; charset=utf-8" {
		t.Fatalf("ExportTodo: %v %v", body.GetContentType(), err)
	}
	watch, err := c.WatchTodo(ctx, &v1.WatchTodoRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if n := recvAll(t, watch.Recv); n != 3 {
		t.Fatalf("WatchTodo: %d frames, want 3", n)
	}
	lookup, err := c.StreamTodoLookup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lookup.Send(&v1.GetTodoRequest{Id: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lookup.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if n := recvAll(t, lookup.Recv); n != 2 {
		t.Fatalf("StreamTodoLookup: %d frames, want 2", n)
	}
	if _, err := c.DeleteTodo(ctx, &v1.DeleteTodoRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetTodo(ctx, &v1.GetTodoRequest{Id: id})
	if msg := wantGRPCError(t, err, 404, "TODO_NOT_FOUND"); msg != "待办事项未找到。" {
		t.Fatalf("message %q, want the zh-CN translation", msg)
	}
	_, err = c.CreateTodo(ctx, &v1.CreateTodoRequest{})
	if msg := wantGRPCError(t, err, 400, "VALIDATOR"); !strings.Contains(msg, "title") {
		t.Fatalf("message %q, want the validation library's text naming the field", msg)
	}
}

// recvAll reads a stream to its end and returns the number of messages.
func recvAll[T any](t *testing.T, recv func() (T, error)) int {
	t.Helper()
	n := 0
	for {
		_, err := recv()
		if errors.Is(err, io.EOF) {
			return n
		}
		if err != nil {
			t.Fatalf("after %d messages: %v", n, err)
		}
		n++
	}
}

// Basis: keel:docs/grpc.md, "Health checks" (server reflection is on).
func testGRPCHealthAndReflection(t *testing.T, e *env) {
	conn, err := grpc.NewClient(e.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx := context.Background()
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health: %v %v", resp.GetStatus(), err)
	}
	services := listServices(t, ctx, conn)
	for _, want := range []string{"api.keel_layout.v1.TodoService", "grpc.health.v1.Health"} {
		if !slices.Contains(services, want) {
			t.Fatalf("reflection lists %v, missing %s", services, want)
		}
	}
}

func listServices(t *testing.T, ctx context.Context, conn *grpc.ClientConn) []string {
	t.Helper()
	stream, err := rpb.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&rpb.ServerReflectionRequest{MessageRequest: &rpb.ServerReflectionRequest_ListServices{}}); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("reflection: %v", err)
	}
	_ = stream.CloseSend()
	var out []string
	for _, s := range resp.GetListServicesResponse().GetService() {
		out = append(out, s.GetName())
	}
	return out
}

// Basis: keel:docs/middleware.md, "Logging" (one line per unary call; 4xx at WARN).
func testGRPCRequestLog(t *testing.T, e *env) {
	ctx := context.Background()
	id := e.create(t, uniq("log"))
	n, _ := strconv.ParseInt(id, 10, 64)
	mark := e.svc.Mark()
	if _, err := e.todo.GetTodo(ctx, &v1.GetTodoRequest{Id: n}); err != nil {
		t.Fatal(err)
	}
	_, _ = e.todo.GetTodo(ctx, &v1.GetTodoRequest{Id: 999999999})
	op := "/api.keel_layout.v1.TodoService/GetTodo"
	e.svc.WaitLogSince(t, mark, timeout, "", map[string]any{"component": "grpc", "operation": op, "level": "INFO"})
	e.svc.WaitLogSince(t, mark, timeout, "", map[string]any{"component": "grpc", "operation": op, "level": "WARN"})
}
