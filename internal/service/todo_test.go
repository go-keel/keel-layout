// Example file: delete the whole file after forking

package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/core/knats"
)

// service tests use the real UseCase with a hand-written fake repo: they verify DTO↔DO conversion, paging
// pass-through and the conversion of filters and sorts, with no infrastructure.

type fakeTodoRepo struct {
	todos  map[int64]*biz.Todo
	nextID int64
	// lastList records the arguments of the last List: checking the default size and the filter and sort conversion
	// needs what was passed down; the response row count only proves "no more than the store holds".
	lastList biz.ListOptions
}

var _ biz.TodoRepo = (*fakeTodoRepo)(nil)

func newFakeTodoRepo() *fakeTodoRepo {
	return &fakeTodoRepo{todos: map[int64]*biz.Todo{}, nextID: 1}
}

func (f *fakeTodoRepo) Create(_ context.Context, todo *biz.Todo) (*biz.Todo, error) {
	created := *todo
	created.ID = f.nextID
	f.nextID++
	f.todos[created.ID] = &created
	return &created, nil
}

func (f *fakeTodoRepo) FindByID(_ context.Context, id int64) (*biz.Todo, error) {
	todo, ok := f.todos[id]
	if !ok {
		return nil, biz.ErrTodoNotFound
	}
	clone := *todo
	return &clone, nil
}

func (f *fakeTodoRepo) List(_ context.Context, opts biz.ListOptions) ([]*biz.Todo, int64, error) {
	f.lastList = opts
	var all []*biz.Todo
	for i := int64(1); i < f.nextID; i++ {
		if t, ok := f.todos[i]; ok {
			if opts.Completed != nil && t.Completed != *opts.Completed {
				continue
			}
			all = append(all, t)
		}
	}
	// total is the full count after filtering, regardless of which rows this page takes. The fake must keep that too,
	// or a defect like "total shrinks with paging" goes undetected.
	total := int64(len(all))
	if opts.Offset >= len(all) {
		return nil, total, nil
	}
	end := min(opts.Offset+opts.Limit, len(all))
	return all[opts.Offset:end], total, nil
}

// Update overwrites only the fields listed in fields: if the fake wrote the whole row back, the contract "fields not
// listed stay unchanged" could not be tested.
func (f *fakeTodoRepo) Update(_ context.Context, todo *biz.Todo, fields []string) (*biz.Todo, error) {
	cur, ok := f.todos[todo.ID]
	if !ok {
		return nil, biz.ErrTodoNotFound
	}
	updated := *cur
	for _, field := range fields {
		switch field {
		case biz.FieldTitle:
			updated.Title = todo.Title
		case biz.FieldContent:
			updated.Content = todo.Content
		case biz.FieldCompleted:
			updated.Completed = todo.Completed
		default:
			return nil, biz.ErrInvalidArgument
		}
	}
	f.todos[todo.ID] = &updated
	return &updated, nil
}

func (f *fakeTodoRepo) Delete(_ context.Context, id int64) error {
	if _, ok := f.todos[id]; !ok {
		return biz.ErrTodoNotFound
	}
	delete(f.todos, id)
	return nil
}

type noopPublisher struct{}

var _ knats.Publisher = (*noopPublisher)(nil)

func (noopPublisher) Publish(context.Context, knats.Stream, string, any) error { return nil }
func (noopPublisher) PublishEx(context.Context, knats.Stream, string, knats.Publishing) error {
	return nil
}

func newTestService() *TodoService {
	svc, _ := newTestServiceWithRepo()
	return svc
}

// newTestServiceWithRepo also returns the fake, for cases that assert what was passed down.
func newTestServiceWithRepo() (*TodoService, *fakeTodoRepo) {
	repo := newFakeTodoRepo()
	return NewTodoService(biz.NewTodoUseCase(repo, noopPublisher{})), repo
}

func TestTodoService_CreateAndGet(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	created, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "hello", Content: "world"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if created.GetId() == 0 || created.GetTitle() != "hello" {
		t.Fatalf("unexpected reply: %+v", created)
	}

	got, err := svc.GetTodo(ctx, &v1.GetTodoRequest{Id: created.GetId()})
	if err != nil {
		t.Fatalf("GetTodo: %v", err)
	}
	if got.GetContent() != "world" {
		t.Fatalf("unexpected content: %q", got.GetContent())
	}
}

// The update_mask contract is "fields not listed stay unchanged". If service did a Get, merged in memory and wrote
// the whole row back, the later of two concurrent PATCHes changing title and completed would overwrite the earlier.
// With the field set passed down, two PATCHes sent from the same old snapshot no longer overwrite each other; the
// test below simulates exactly that sequence.
func TestTodoService_UpdateTodo_ConcurrentPatchesDoNotClobber(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	created, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "before", Content: "c"})
	if err != nil {
		t.Fatal(err)
	}

	// A and B both read the same old snapshot (completed=false, title="before") and each change one field.
	if _, err := svc.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: created.GetId(), Title: "after"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	}); err != nil {
		t.Fatalf("patch A: %v", err)
	}
	if _, err := svc.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: created.GetId(), Completed: true},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}},
	}); err != nil {
		t.Fatalf("patch B: %v", err)
	}

	got, err := svc.GetTodo(ctx, &v1.GetTodoRequest{Id: created.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTitle() != "after" {
		t.Fatalf("title = %q, want \"after\" (B listed only completed and must not touch title)", got.GetTitle())
	}
	if !got.GetCompleted() {
		t.Fatal("completed = false, want true")
	}
	if got.GetContent() != "c" {
		t.Fatalf("content = %q, want \"c\" (neither PATCH listed content)", got.GetContent())
	}
}

// An unknown field path in the mask must return InvalidArgument, not be silently ignored.
func TestTodoService_UpdateTodo_UnknownMaskPath(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	created, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: created.GetId(), Title: "x"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nope"}},
	})
	if !errors.Is(err, biz.ErrInvalidArgument) {
		t.Fatalf("got %v, want ErrInvalidArgument", err)
	}
}

func TestTodoService_ListTodo_Pagination(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	for range 5 {
		if _, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := svc.ListTodo(ctx, &v1.ListTodoRequest{Offset: 0, Limit: 2})
	if err != nil {
		t.Fatalf("ListTodo page1: %v", err)
	}
	if len(page1.GetTodos()) != 2 {
		t.Fatalf("page1: got %d todos, want 2", len(page1.GetTodos()))
	}

	page2, err := svc.ListTodo(ctx, &v1.ListTodoRequest{Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("ListTodo page2: %v", err)
	}
	if len(page2.GetTodos()) != 2 {
		t.Fatalf("page2: got %d todos, want 2", len(page2.GetTodos()))
	}
	if page1.GetTodos()[0].GetId() == page2.GetTodos()[0].GetId() {
		t.Fatal("pages overlap")
	}

	// An out-of-range offset gives an empty page, not an error: a pager past the last page is normal paging.
	tail, err := svc.ListTodo(ctx, &v1.ListTodoRequest{Offset: 99, Limit: 2})
	if err != nil {
		t.Fatalf("ListTodo out-of-range offset: %v", err)
	}
	if len(tail.GetTodos()) != 0 {
		t.Fatalf("out-of-range offset should give an empty page, got %d rows", len(tail.GetTodos()))
	}
}

// total is "how many match in all", not "how many on this page". If it shrank with paging, the caller's pager would
// compute a single page: requests still go out and the page still renders, only the count is wrong.
func TestTodoService_ListTodo_TotalIgnoresPaging(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	for range 5 {
		if _, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}

	for _, req := range []*v1.ListTodoRequest{
		{Offset: 0, Limit: 2},
		{Offset: 2, Limit: 2},
		{Offset: 4, Limit: 2},
		{Offset: 99, Limit: 2},
		{},
	} {
		resp, err := svc.ListTodo(ctx, req)
		if err != nil {
			t.Fatalf("offset=%d limit=%d: %v", req.GetOffset(), req.GetLimit(), err)
		}
		if resp.GetTotal() != 5 {
			t.Fatalf("offset=%d limit=%d: total = %d, want 5", req.GetOffset(), req.GetLimit(), resp.GetTotal())
		}
	}
}

// For limit 0 biz fills the default; the upper bound belongs only to the proto lte and is not clamped in code. A
// direct Go call passes an out-of-range value to the repo unchanged, on purpose: in-process callers own their values.
func TestTodoService_LimitPassedThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*TodoService, context.Context) error
		want int
	}{
		{"ListTodo without a value uses the default", func(s *TodoService, ctx context.Context) error {
			_, err := s.ListTodo(ctx, &v1.ListTodoRequest{})
			return err
		}, 20},
		{"ListTodo passes a given value through", func(s *TodoService, ctx context.Context) error {
			_, err := s.ListTodo(ctx, &v1.ListTodoRequest{Limit: 500})
			return err
		}, 500},
		{"ExportTodo without a value uses the export default", func(s *TodoService, ctx context.Context) error {
			_, err := s.ExportTodo(ctx, &v1.ExportTodoRequest{})
			return err
		}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestServiceWithRepo()
			if err := tc.call(svc, context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := repo.lastList.Limit; got != tc.want {
				t.Fatalf("Limit passed down = %d, want %d", got, tc.want)
			}
		})
	}
}

// completed is tri-state: 0 all, 1 yes, 2 no.
func TestTodoService_ListTodo_CompletedFilter(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	created, _ := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "done"})
	if _, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: created.GetId(), Title: "done", Completed: true},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		completed int32
		want      []string
	}{
		{0, []string{"done", "pending"}},
		{1, []string{"done"}},
		{2, []string{"pending"}},
	} {
		resp, err := svc.ListTodo(ctx, &v1.ListTodoRequest{Completed: tc.completed})
		if err != nil {
			t.Fatalf("completed=%d: %v", tc.completed, err)
		}
		var got []string
		for _, item := range resp.GetTodos() {
			got = append(got, item.GetTitle())
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("completed=%d: got %v, want %v", tc.completed, got, tc.want)
		}
		if resp.GetTotal() != int64(len(tc.want)) {
			t.Fatalf("completed=%d: total = %d, want %d", tc.completed, resp.GetTotal(), len(tc.want))
		}
	}

	// The transport's proto validation rejects out-of-range values; a direct call reaching the conversion is rejected too, not treated as "all".
	if _, err := svc.ListTodo(ctx, &v1.ListTodoRequest{Completed: 3}); !errors.Is(err, biz.ErrInvalidArgument) {
		t.Fatalf("completed=3 should fail, got %v", err)
	}
}

func TestTodoService_ListTodo_Sort(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sort    v1.ListTodoRequest_Sort
		want    biz.TodoSort
		wantErr error
	}{
		{"unset uses the server default", v1.ListTodoRequest_SORT_UNSPECIFIED, biz.TodoSortDefault, nil},
		{"most recently updated first", v1.ListTodoRequest_SORT_UPDATE_TIME_DESC, biz.TodoSortUpdateTimeDesc, nil},
		{"undefined value", v1.ListTodoRequest_Sort(99), "", biz.ErrInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestServiceWithRepo()
			_, err := svc.ListTodo(context.Background(), &v1.ListTodoRequest{Sort: tc.sort})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && repo.lastList.Sort != tc.want {
				t.Fatalf("Sort passed down = %q, want %q", repo.lastList.Sort, tc.want)
			}
		})
	}
}

func TestTodoService_UpdateTodo_FieldMask(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	created, _ := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "orig", Content: "keep"})

	updated, err := svc.UpdateTodo(ctx, &v1.UpdateTodoRequest{
		Todo:       &v1.Todo{Id: created.GetId(), Title: "renamed"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	})
	if err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	if updated.GetTitle() != "renamed" || updated.GetContent() != "keep" {
		t.Fatalf("field mask not respected: %+v", updated)
	}
}

func TestTodoService_DeleteTodo_NotFound(t *testing.T) {
	svc := newTestService()
	_, err := svc.DeleteTodo(context.Background(), &v1.DeleteTodoRequest{Id: 999})
	if !errors.Is(err, biz.ErrTodoNotFound) {
		t.Fatalf("expected ErrTodoNotFound, got %v", err)
	}
}

// fakeWatchStream implements v1.TodoService_WatchTodoServer (= grpc.ServerStreamingServer[Todo]).
type fakeWatchStream struct {
	grpc.ServerStream // only to satisfy the interface; the unused methods are never called
	ctx               context.Context
	sent              []*v1.Todo
}

func (f *fakeWatchStream) Context() context.Context { return f.ctx }
func (f *fakeWatchStream) Send(t *v1.Todo) error {
	f.sent = append(f.sent, t)
	return nil
}

func TestTodoService_WatchTodo_Snapshot(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	for range 3 {
		if _, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "w"}); err != nil {
			t.Fatal(err)
		}
	}

	stream := &fakeWatchStream{ctx: ctx}
	if err := svc.WatchTodo(&v1.WatchTodoRequest{Limit: 2}, stream); err != nil {
		t.Fatalf("WatchTodo: %v", err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("expected 2 events, got %d", len(stream.sent))
	}
}

// validate does not run when gRPC opens a stream, so WatchTodo applies the proto rule (limit at most 100) itself; 0 uses
// the default.
func TestTodoService_WatchTodo_ValidatesLimit(t *testing.T) {
	svc, repo := newTestServiceWithRepo()
	ctx := context.Background()

	if err := svc.WatchTodo(&v1.WatchTodoRequest{Limit: 101}, &fakeWatchStream{ctx: ctx}); !errors.Is(err, biz.ErrInvalidArgument) {
		t.Fatalf("over the limit should be rejected, got %v", err)
	}
	if err := svc.WatchTodo(&v1.WatchTodoRequest{}, &fakeWatchStream{ctx: ctx}); err != nil {
		t.Fatalf("WatchTodo: %v", err)
	}
	if repo.lastList.Limit != 20 {
		t.Fatalf("Limit passed down = %d, want 20", repo.lastList.Limit)
	}
}

// fakeLookupStream implements v1.TodoService_StreamTodoLookupServer
// (= grpc.BidiStreamingServer[GetTodoRequest, Todo]).
type fakeLookupStream struct {
	grpc.ServerStream
	ctx  context.Context
	in   []*v1.GetTodoRequest
	sent []*v1.Todo
}

func (f *fakeLookupStream) Context() context.Context { return f.ctx }
func (f *fakeLookupStream) Recv() (*v1.GetTodoRequest, error) {
	if len(f.in) == 0 {
		return nil, io.EOF
	}
	req := f.in[0]
	f.in = f.in[1:]
	return req, nil
}
func (f *fakeLookupStream) Send(t *v1.Todo) error {
	f.sent = append(f.sent, t)
	return nil
}

func TestTodoService_StreamTodoLookup(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	created, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: "lookup-me"})
	if err != nil {
		t.Fatal(err)
	}

	stream := &fakeLookupStream{
		ctx: ctx,
		in:  []*v1.GetTodoRequest{{Id: created.GetId()}},
	}
	if err := svc.StreamTodoLookup(stream); err != nil {
		t.Fatalf("StreamTodoLookup: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetTitle() != "lookup-me" {
		t.Fatalf("unexpected replies: %+v", stream.sent)
	}
}

func TestTodoService_StreamTodoLookup_NotFound(t *testing.T) {
	svc := newTestService()
	stream := &fakeLookupStream{
		ctx: context.Background(),
		in:  []*v1.GetTodoRequest{{Id: 404}},
	}
	if err := svc.StreamTodoLookup(stream); !errors.Is(err, biz.ErrTodoNotFound) {
		t.Fatalf("expected ErrTodoNotFound, got %v", err)
	}
}

// Assert the parsed meaning, not the bytes: whether to quote is up to encoding/csv; "the title reads back unchanged"
// is the contract of this endpoint. Titles with quotes, newlines and commas must all pass.
func TestTodoService_ExportTodo_CSV(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	titles := []string{"export-me", `say "hi"`, "line1\nline2", "a,b"}
	for _, title := range titles {
		if _, err := svc.CreateTodo(ctx, &v1.CreateTodoRequest{Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	body, err := svc.ExportTodo(ctx, &v1.ExportTodoRequest{})
	if err != nil {
		t.Fatalf("ExportTodo: %v", err)
	}
	if !strings.HasPrefix(body.GetContentType(), "text/csv") {
		t.Fatalf("content-type = %q", body.GetContentType())
	}

	rows, err := csv.NewReader(bytes.NewReader(body.GetData())).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, body.GetData())
	}
	if len(rows) != len(titles)+1 {
		t.Fatalf("got %d rows (including header), want %d", len(rows), len(titles)+1)
	}
	if got := strings.Join(rows[0], ","); got != "id,title,completed" {
		t.Fatalf("header = %q", got)
	}
	for i, want := range titles {
		if got := rows[i+1][1]; got != want {
			t.Fatalf("row %d title = %q, want %q", i+1, got, want)
		}
	}
}
