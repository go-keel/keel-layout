//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
)

// sseData returns the data: payloads of an SSE body.
func sseData(body []byte) []string {
	var out []string
	for _, l := range lines(body) {
		if d, ok := strings.CutPrefix(l, "data:"); ok {
			out = append(out, strings.TrimSpace(d))
		}
	}
	return out
}

// ensure makes sure the table holds at least n rows.
func (e *env) ensure(t *testing.T, n int64) {
	t.Helper()
	for i := e.count(t); i < n; i++ {
		e.create(t, uniq("filler"))
	}
}

// Basis: keel:docs/http.md, "1b" (server stream as SSE); todo.proto, WatchTodo.
func testSSEWatch(t *testing.T, e *env) {
	e.ensure(t, 20)
	r := e.call(t, http.MethodGet, "/v1/todos/watch?limit=3", "").want(t, 200)
	if !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type %q", r.header.Get("Content-Type"))
	}
	frames := sseData(r.body)
	if len(frames) != 3 {
		t.Fatalf("%d frames, want 3:\n%s", len(frames), r.body)
	}
	for _, f := range frames {
		var m map[string]any
		if err := json.Unmarshal([]byte(f), &m); err != nil {
			t.Fatalf("frame %q: %v", f, err)
		}
		if _, ok := m["id"].(string); !ok {
			t.Fatalf("frame id is not a string: %s", f)
		}
	}
	if n := len(sseData(e.call(t, http.MethodGet, "/v1/todos/watch", "").want(t, 200).body)); n != 20 {
		t.Fatalf("default limit: %d frames, want 20", n)
	}
	e.call(t, http.MethodGet, "/v1/todos/watch?limit=101", "").wantError(t, 400, "VALIDATOR")
}

// Basis: keel:docs/grpc.md, "Streams run the chain once per stream" (stream messages are not validated); todo.proto, WatchTodo and StreamTodoLookup.
func testGRPCStreams(t *testing.T, e *env) {
	ctx := context.Background()
	e.ensure(t, 3)
	w, err := e.todo.WatchTodo(ctx, &v1.WatchTodoRequest{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if n := recvAll(t, w.Recv); n != 3 {
		t.Fatalf("WatchTodo: %d messages, want 3", n)
	}
	w, err = e.todo.WatchTodo(ctx, &v1.WatchTodoRequest{Limit: 101})
	if err == nil {
		_, err = w.Recv()
	}
	if msg := wantGRPCError(t, err, 400, "INVALID_ARGUMENT"); msg != "Invalid request. Please check your input." {
		t.Fatalf("message %q", msg)
	}

	a, _ := strconv.ParseInt(e.create(t, uniq("lookup a")), 10, 64)
	b, _ := strconv.ParseInt(e.create(t, uniq("lookup b")), 10, 64)
	s, err := e.todo.StreamTodoLookup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if err := s.Send(&v1.GetTodoRequest{Id: id}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.CloseSend()
	for _, want := range []int64{a, b} {
		got, err := s.Recv()
		if err != nil || got.GetId() != want {
			t.Fatalf("lookup: got %v %v, want %d", got.GetId(), err, want)
		}
	}
	s, err = e.todo.StreamTodoLookup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Send(&v1.GetTodoRequest{Id: 0})
	_ = s.CloseSend()
	_, err = s.Recv()
	wantGRPCError(t, err, 404, "TODO_NOT_FOUND")
}

func (e *env) dialWS(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws://"+e.httpAddr+path, nil)
	if err != nil {
		t.Fatalf("dial %s: %v (%v)", path, err, resp)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	return c
}

// Basis: keel:docs/http.md, "1c" (bidirectional stream as WebSocket; an error mid-stream is a \x1e error: frame and
// close code 1011); todo.proto, StreamTodoLookup.
func testWebSocketLookup(t *testing.T, e *env) {
	a, b := e.create(t, uniq("ws a")), e.create(t, uniq("ws b"))
	c := e.dialWS(t, "/v1/todos/lookup")
	for _, id := range []string{a, b} {
		if err := c.WriteMessage(websocket.TextMessage, []byte(`{"id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{a, b} {
		mt, data, err := c.ReadMessage()
		var m map[string]any
		if err != nil || mt != websocket.TextMessage || json.Unmarshal(data, &m) != nil || m["id"] != want {
			t.Fatalf("frame %q %v, want todo %s", data, err, want)
		}
	}
	_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	c = e.dialWS(t, "/v1/todos/lookup")
	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"id":"`+a+`"}`))
	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"id":"999999999"}`))
	if _, data, err := c.ReadMessage(); err != nil || !strings.Contains(string(data), `"`+a+`"`) {
		t.Fatalf("first frame %q %v", data, err)
	}
	_, data, err := c.ReadMessage()
	if err != nil || !strings.HasPrefix(string(data), "\x1eerror:") || !strings.Contains(string(data), "code = 404 reason = TODO_NOT_FOUND") {
		t.Fatalf("error frame %q %v", data, err)
	}
	_, _, err = c.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseInternalServerErr) {
		t.Fatalf("close: %v, want 1011", err)
	}
}

// Basis: keel:docs/http.md, "1d" (HttpBody); todo.proto, ExportTodo; internal/service/todo.go, ExportTodo (CSV escaping).
func testExportCSV(t *testing.T, e *env) {
	title := `a,"b" ` + uniq("csv")
	e.create(t, title)
	r := e.call(t, http.MethodGet, "/v1/todos/export", "").want(t, 200)
	if ct := r.header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	recs, err := csv.NewReader(strings.NewReader(string(r.body))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(recs[0], ",") != "id,title,completed" || int64(len(recs)-1) != min(e.count(t), 100) {
		t.Fatalf("header %v, %d rows (database %d)", recs[0], len(recs)-1, e.count(t))
	}
	r = e.call(t, http.MethodGet, "/v1/todos/export?limit=1000", "").want(t, 200)
	if !strings.Contains(string(r.body), `,"a,""b"" `) {
		t.Fatalf("the title %q is not escaped as CSV", title)
	}
	e.call(t, http.MethodGet, "/v1/todos/export?limit=1001", "").wantError(t, 400, "VALIDATOR")

	body, err := e.todo.ExportTodo(context.Background(), &v1.ExportTodoRequest{Limit: 2})
	if err != nil || body.GetContentType() != "text/csv; charset=utf-8" || len(lines(body.GetData())) != 3 {
		t.Fatalf("gRPC ExportTodo: %q %q %v", body.GetContentType(), body.GetData(), err)
	}
}
