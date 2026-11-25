//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
)

// Basis: keel:docs/http.md, "Error responses"; keel:docs/middleware.md, "Locale" and "Validate";
// internal/service/errors.go.
func testErrorBodyAndLocale(t *testing.T, e *env) {
	r := e.call(t, http.MethodGet, "/v1/todos/999999999", "").want(t, 404)
	// Compact first: protojson adds random spaces that change with the binary, so a byte-for-byte check breaks on
	// unrelated edits. encoding/json because pkg/json has no Compact.
	var got bytes.Buffer
	if err := json.Compact(&got, r.body); err != nil || got.String() != `{"code":404,"reason":"TODO_NOT_FOUND","message":"Todo not found.","metadata":{}}` {
		t.Fatalf("error body %s (%v)", r.body, err)
	}
	for lang, want := range map[string]string{"zh-CN": "待办事项未找到。", "ja": "Todo not found.", "xx": "Todo not found."} {
		if msg := e.call(t, http.MethodGet, "/v1/todos/999999999", "", "Accept-Language", lang).wantError(t, 404, "TODO_NOT_FOUND"); msg != want {
			t.Fatalf("%s: %q, want %q", lang, msg, want)
		}
	}

	required := map[string]string{}
	for _, lang := range []string{"", "zh-CN", "ja"} {
		msg := e.call(t, http.MethodPost, "/v1/todos", `{"title":""}`, "Accept-Language", lang).wantError(t, 400, "VALIDATOR")
		if strings.Contains(msg, "title") || strings.Contains(msg, "min_len") {
			t.Fatalf("%q: the message names the field or rule: %q", lang, msg)
		}
		required[lang] = msg
	}
	if required[""] == required["zh-CN"] || required["zh-CN"] == required["ja"] || required[""] == required["ja"] || !containsHan(required["zh-CN"]) {
		t.Fatalf("not one sentence per language: %q", required)
	}
	for header, want := range map[string]string{"fr, zh-CN;q=0.1": required["zh-CN"], "en;q=0.1, zh-CN": required[""]} {
		if msg := e.call(t, http.MethodPost, "/v1/todos", `{"title":""}`, "Accept-Language", header).wantError(t, 400, "VALIDATOR"); msg != want {
			t.Fatalf("Accept-Language %q: %q, want %q", header, msg, want)
		}
	}
	long := e.call(t, http.MethodPost, "/v1/todos", mustJSON(map[string]any{"title": repeat("测", 201)}), "Accept-Language", "zh-CN").wantError(t, 400, "VALIDATOR")
	if long == required["zh-CN"] || !containsHan(long) {
		t.Fatalf("a length violation reads %q, the same as a missing value", long)
	}
	id := e.create(t, uniq("locale"))
	if msg := e.call(t, http.MethodPatch, "/v1/todos/"+id+"?update_mask=nope", `{"title":"x"}`, "Accept-Language", "zh-CN").wantError(t, 400, "INVALID_ARGUMENT"); msg != "输入有误，请检查后再试。" {
		t.Fatalf("INVALID_ARGUMENT in zh-CN: %q", msg)
	}
}

// Basis: keel:docs/middleware.md, "Sentinel" and "Logging" (5xx logged at ERROR); docs/guides/errors.md, "HTTP status
// codes" (an unexpected error reveals nothing).
func testDatabaseErrorHidden(t *testing.T, e *env) {
	// ALTER TABLE ... RENAME TO is the same statement on MySQL and PostgreSQL.
	if err := e.db.Exec("ALTER TABLE todo RENAME TO todo_gone").Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := e.db.Exec("ALTER TABLE todo_gone RENAME TO todo").Error; err != nil {
			t.Fatalf("restore the todo table: %v", err)
		}
	}()
	mark := e.api.Mark()
	for lang, want := range map[string]string{"": "Something went wrong. Please try again later.", "zh-CN": "出错了，请稍后再试。"} {
		r := e.call(t, http.MethodGet, "/v1/todos", "", "Accept-Language", lang)
		if msg := r.wantError(t, 500, "UNEXPECTED_ERROR"); msg != want {
			t.Fatalf("%q: %q, want %q", lang, msg, want)
		}
		if b := strings.ToLower(string(r.body)); strings.Contains(b, "todo_gone") || strings.Contains(b, "1146") || strings.Contains(b, "42p01") {
			t.Fatalf("the response leaks the database error: %s", r.body)
		}
	}
	e.api.WaitLogSince(t, mark, timeout, "failed to count todos", map[string]any{"level": "ERROR"})
	e.api.WaitLogSince(t, mark, timeout, "/v1/todos", map[string]any{"kind": "server", "level": "ERROR"})
	_, err := e.todo.ListTodo(context.Background(), &v1.ListTodoRequest{})
	wantGRPCError(t, err, 500, "UNEXPECTED_ERROR")
}
