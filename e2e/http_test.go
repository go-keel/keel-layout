//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
)

// Basis: keel:docs/messaging.md, "New durables start from new messages".
func testDurableStartsFromNewEvents(t *testing.T, e *env) {
	c, err := e.js.Consumer(context.Background(), todoStream(t), todoDurable)
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.CachedInfo().Config
	if cfg.DeliverPolicy != jetstream.DeliverNewPolicy || cfg.AckWait != 30*time.Second || cfg.MaxDeliver != 4 || cfg.FilterSubject != todoSubject(t) {
		t.Fatalf("durable config: deliver %v, ack wait %s, max deliver %d, filter %q", cfg.DeliverPolicy, cfg.AckWait, cfg.MaxDeliver, cfg.FilterSubject)
	}
	s, err := e.js.Stream(context.Background(), todoStream(t))
	if err != nil {
		t.Fatal(err)
	}
	si := s.CachedInfo()
	if len(si.Config.Subjects) != 1 || si.Config.Subjects[0] != domain(t)+".todo.>" && si.Config.Subjects[0] != todoSubject(t) ||
		si.Config.MaxAge != 7*24*time.Hour || si.Config.Replicas != 1 {
		t.Fatalf("stream config: subjects %v, max age %s, replicas %d", si.Config.Subjects, si.Config.MaxAge, si.Config.Replicas)
	}
	if _, err := e.js.Stream(context.Background(), "DEAD_LETTER"); err != nil {
		t.Fatalf("DEAD_LETTER stream: %v", err)
	}
	// Events are delivered in order: once a later Todo is handled, the one created before the durable existed would
	// have been handled first if it were delivered at all.
	mark := 0
	id := e.create(t, uniq("after the worker"))
	e.worker.WaitLog(t, "todo_created event handled", map[string]any{"todo_id": id})
	if l := e.worker.FindSince(mark, "todo_created event handled", map[string]any{"todo_id": e.preID}); l != nil {
		t.Fatalf("the event published before the durable existed was handled: %v", l)
	}
}

// Basis: keel:docs/http.md, "JSON encoding is protojson"; api/keel_layout/v1/todo.proto, CreateTodo.
func testHTTPCreate(t *testing.T, e *env) {
	title := uniq("create 测试")
	m := e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": title, "content": "c1"})).want(t, 200).json(t)
	id, ok := m["id"].(string)
	if !ok || id == "" {
		t.Fatalf("id is not a string: %v", m)
	}
	if m["title"] != title || m["content"] != "c1" || m["completed"] != false {
		t.Fatalf("created todo: %v", m)
	}
	for _, f := range []string{"createTime", "updateTime"} {
		s, _ := m[f].(string)
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || !strings.HasSuffix(s, "Z") || time.Since(ts) > time.Minute || time.Since(ts) < -time.Minute {
			t.Fatalf("%s = %q is not a current RFC 3339 UTC time (%v)", f, s, err)
		}
	}
	var row struct {
		Title     string
		Content   string
		Completed bool
	}
	if err := e.db.Table("todo").Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Title != title || row.Content != "c1" || row.Completed {
		t.Fatalf("row in the database: %+v", row)
	}
}

func (e *env) count(t *testing.T, where ...any) int64 {
	t.Helper()
	var n int64
	q := e.db.Table("todo")
	if len(where) > 0 {
		q = q.Where(where[0], where[1:]...)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func rows(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := m["todos"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func id64(t *testing.T, v any) int64 {
	t.Helper()
	n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
	if err != nil {
		t.Fatalf("id %v: %v", v, err)
	}
	return n
}

// Basis: api/keel_layout/v1/todo.proto, ListTodo, ListTodoRequest.limit and TodoListItem.
func testHTTPList(t *testing.T, e *env) {
	for i := 0; i < 21; i++ {
		e.create(t, uniq("list"))
	}
	total := e.count(t)
	m := e.call(t, http.MethodGet, "/v1/todos", "").want(t, 200).json(t)
	rs := rows(t, m)
	if m["total"] != strconv.FormatInt(total, 10) || len(rs) != 20 {
		t.Fatalf("default page: total %v (database %d), %d rows, want 20", m["total"], total, len(rs))
	}
	for i, r := range rs {
		if _, ok := r["content"]; ok {
			t.Fatalf("a list row carries content: %v", r)
		}
		if i > 0 && id64(t, r["id"]) <= id64(t, rs[i-1]["id"]) {
			t.Fatalf("rows not in ascending id order at %d", i)
		}
	}
	m = e.call(t, http.MethodGet, fmt.Sprintf("/v1/todos?offset=%d&limit=10", total-1), "").want(t, 200).json(t)
	if len(rows(t, m)) != 1 {
		t.Fatalf("offset total-1: %d rows, want 1", len(rows(t, m)))
	}
	m = e.call(t, http.MethodGet, "/v1/todos?limit=100", "").want(t, 200).json(t)
	if want := min(total, 100); int64(len(rows(t, m))) != want {
		t.Fatalf("limit=100: %d rows, want %d", len(rows(t, m)), want)
	}
	e.call(t, http.MethodGet, "/v1/todos?limit=101", "").wantError(t, 400, "VALIDATOR")
}

// Basis: api/keel_layout/v1/todo.proto, ListTodoRequest.completed and ListTodoRequest.Sort.
func testHTTPListFiltersAndSort(t *testing.T, e *env) {
	id := e.create(t, uniq("done"))
	e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=completed", `{"completed":true}`).want(t, 200)
	for q, want := range map[string]int64{"1": e.count(t, "completed = ?", true), "2": e.count(t, "completed = ?", false)} {
		m := e.call(t, http.MethodGet, "/v1/todos?completed="+q, "").want(t, 200).json(t)
		if m["total"] != strconv.FormatInt(want, 10) {
			t.Fatalf("completed=%s: total %v, want %d", q, m["total"], want)
		}
	}
	e.call(t, http.MethodGet, "/v1/todos?completed=3", "").wantError(t, 400, "VALIDATOR")

	other := e.create(t, uniq("sorted"))
	e.call(t, http.MethodPatch, "/v1/todos/"+other+"?update_mask=title", mustJSON(map[string]any{"title": uniq("sorted v2")})).want(t, 200)
	m := e.call(t, http.MethodGet, "/v1/todos?sort=SORT_UPDATE_TIME_DESC&limit=1", "").want(t, 200).json(t)
	if rs := rows(t, m); len(rs) != 1 || rs[0]["id"] != other {
		t.Fatalf("first row by update time is %v, want %s", rs, other)
	}
}

// Basis: keel:docs/http.md, "A query value that cannot be decoded"; todo.proto defined_only and offset gte 0.
func testHTTPListBadQuery(t *testing.T, e *env) {
	msg := e.call(t, http.MethodGet, "/v1/todos?sort=5", "", "Accept-Language", "zh-CN").wantError(t, 400, "CODEC")
	if !strings.Contains(msg, `"sort"`) || !strings.Contains(msg, "not a valid value") {
		t.Fatalf("CODEC message %q is not the decoder's English text", msg)
	}
	e.call(t, http.MethodGet, "/v1/todos?offset=-1", "").wantError(t, 400, "VALIDATOR")
	_, err := e.todo.ListTodo(context.Background(), &v1.ListTodoRequest{Sort: 5})
	wantGRPCError(t, err, 400, "VALIDATOR")
}

// Basis: todo.proto, UpdateTodo ("fields not listed stay unchanged"); internal/biz/todo.go, UpdateTodo.
func testHTTPUpdateMask(t *testing.T, e *env) {
	m := e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": uniq("mask"), "content": "c1"})).want(t, 200).json(t)
	id := m["id"].(string)
	title := uniq("mask v2")
	m = e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=title", mustJSON(map[string]any{"title": title, "content": "ignored"})).want(t, 200).json(t)
	if m["title"] != title || m["content"] != "c1" {
		t.Fatalf("update_mask=title: %v", m)
	}
	e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=completed", `{"completed":true}`).want(t, 200)
	m = e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=completed", `{"completed":false}`).want(t, 200).json(t)
	if m["completed"] != false || e.count(t, "id = ? AND completed = ?", id, false) != 1 {
		t.Fatalf("zero value not written: %v", m)
	}
	star := uniq("star")
	m = e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=*", mustJSON(map[string]any{"title": star})).want(t, 200).json(t)
	if m["title"] != star || m["content"] != "" || m["completed"] != false {
		t.Fatalf("update_mask=*: %v", m)
	}
	e.call(t, http.MethodPatch, "/v1/todos/"+id, `{"title":"x"}`).wantError(t, 400, "VALIDATOR")
	e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=nope", `{"title":"x"}`).wantError(t, 400, "INVALID_ARGUMENT")
	e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=title", mustJSON(map[string]any{"title": repeat("a", 201)})).wantError(t, 400, "INVALID_ARGUMENT")
	zh := repeat("测", 200)
	e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=title", mustJSON(map[string]any{"title": zh})).want(t, 200)
	if e.count(t, "id = ? AND title = ?", id, zh) != 1 {
		t.Fatal("the 200-character Chinese title was not stored whole")
	}
	e.call(t, http.MethodPatch, "/v1/todos/999999999?update_mask=title", `{"title":"x"}`).wantError(t, 404, "TODO_NOT_FOUND")
}

// Basis: internal/data/todo.go, Update (no row matched is TODO_NOT_FOUND; a row whose values do not change is not).
func testHTTPUpdateUnchangedValue(t *testing.T, e *env) {
	title := uniq("unchanged")
	id := e.create(t, title)
	for i := 0; i < 5; i++ {
		e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=title,completed", mustJSON(map[string]any{"title": title, "completed": false})).want(t, 200)
	}
}

// Basis: todo.proto, CreateTodoRequest; internal/biz/todo.go, lengths count characters; keel:docs/http.md, protojson
// discards unknown fields.
func testHTTPCreateValidation(t *testing.T, e *env) {
	zh := repeat("测", 199) + "x"
	e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": zh})).want(t, 200)
	e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": repeat("测", 201)})).wantError(t, 400, "VALIDATOR")
	e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": "x", "content": repeat("c", 2001)})).wantError(t, 400, "VALIDATOR")
	e.call(t, http.MethodPost, "/v1/todos", `{"title":""}`).wantError(t, 400, "VALIDATOR")
	e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": uniq("unknown"), "nope": 1})).want(t, 200)
}

// Basis: todo.proto, GetTodo and DeleteTodo; GetTodo's comment (fixed paths are declared first).
func testHTTPGetDelete(t *testing.T, e *env) {
	title := uniq("get")
	id := e.create(t, title)
	if m := e.call(t, http.MethodGet, "/v1/todos/"+id, "").want(t, 200).json(t); m["id"] != id || m["title"] != title {
		t.Fatalf("get: %v", m)
	}
	e.call(t, http.MethodGet, "/v1/todos/999999999", "").wantError(t, 404, "TODO_NOT_FOUND")
	e.call(t, http.MethodGet, "/v1/todos/0", "").wantError(t, 400, "VALIDATOR")

	if r := e.call(t, http.MethodDelete, "/v1/todos/"+id, "").want(t, 200); strings.TrimSpace(string(r.body)) != "{}" {
		t.Fatalf("delete body %q, want {}", r.body)
	}
	if e.count(t, "id = ?", id) != 0 {
		t.Fatal("row still in the database after delete")
	}
	e.call(t, http.MethodDelete, "/v1/todos/"+id, "").wantError(t, 404, "TODO_NOT_FOUND")
	e.call(t, http.MethodDelete, "/v1/todos/0", "").wantError(t, 400, "VALIDATOR")

	if r := e.call(t, http.MethodGet, "/v1/todos/watch?limit=1", "").want(t, 200); !strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("/v1/todos/watch answered %s, not the SSE stream", r.header.Get("Content-Type"))
	}
}
