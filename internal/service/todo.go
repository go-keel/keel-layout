// Example file: delete the whole file after forking

package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"

	"google.golang.org/genproto/googleapis/api/httpbody"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"buf.build/go/protovalidate"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/biz"
)

// TodoService is the transport adapter layer: DTO ↔ DO conversion, no business rules.
type TodoService struct {
	v1.UnsafeTodoServiceServer

	uc *biz.TodoUseCase
}

func NewTodoService(uc *biz.TodoUseCase) *TodoService {
	return &TodoService{uc: uc}
}

func (s *TodoService) CreateTodo(ctx context.Context, req *v1.CreateTodoRequest) (*v1.Todo, error) {
	todo, err := s.uc.CreateTodo(ctx, &biz.Todo{
		Title:   req.GetTitle(),
		Content: req.GetContent(),
	})
	if err != nil {
		return nil, err
	}
	return toProtoTodo(todo), nil
}

func (s *TodoService) GetTodo(ctx context.Context, req *v1.GetTodoRequest) (*v1.Todo, error) {
	todo, err := s.uc.GetTodo(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return toProtoTodo(todo), nil
}

func (s *TodoService) ListTodo(ctx context.Context, req *v1.ListTodoRequest) (*v1.ListTodoReply, error) {
	completed, err := toBizCompleted(req.GetCompleted())
	if err != nil {
		return nil, err
	}
	sort, err := toBizTodoSort(req.GetSort())
	if err != nil {
		return nil, err
	}

	// offset/limit pass through unchanged: biz fills the default for limit 0, and the transport rejects values above
	// the proto lte; this layer neither converts nor clamps.
	todos, total, err := s.uc.ListTodo(ctx, biz.ListOptions{
		Completed: completed,
		Sort:      sort,
		Offset:    int(req.GetOffset()),
		Limit:     int(req.GetLimit()),
	})
	if err != nil {
		return nil, err
	}

	resp := &v1.ListTodoReply{
		Todos: make([]*v1.TodoListItem, 0, len(todos)),
		Total: total,
	}
	for _, todo := range todos {
		resp.Todos = append(resp.Todos, toProtoTodoListItem(todo))
	}
	return resp, nil
}

func (s *TodoService) UpdateTodo(ctx context.Context, req *v1.UpdateTodoRequest) (*v1.Todo, error) {
	target := req.GetTodo()
	if target.GetId() <= 0 || len(req.GetUpdateMask().GetPaths()) == 0 {
		return nil, biz.ErrInvalidArgument
	}

	// FieldMask → field set, passed all the way to data, which issues a single UPDATE.
	//
	// **No read-modify-write**: with Get, an in-memory merge and a whole-row write, when two concurrent PATCHes change
	// title and completed the later overwrites the earlier, while the update_mask contract is "fields not listed stay
	// unchanged". The biz allowlist checks that field names are valid.
	fields := make([]string, 0, len(req.GetUpdateMask().GetPaths()))
	for _, path := range req.GetUpdateMask().GetPaths() {
		if path == "*" {
			fields = append(fields, biz.FieldTitle, biz.FieldContent, biz.FieldCompleted)
			continue
		}
		fields = append(fields, path)
	}

	todo, err := s.uc.UpdateTodo(ctx, &biz.Todo{
		ID:        target.GetId(),
		Title:     target.GetTitle(),
		Content:   target.GetContent(),
		Completed: target.GetCompleted(),
	}, fields)
	if err != nil {
		return nil, err
	}
	return toProtoTodo(todo), nil
}

func (s *TodoService) DeleteTodo(ctx context.Context, req *v1.DeleteTodoRequest) (*emptypb.Empty, error) {
	if err := s.uc.DeleteTodo(ctx, req.GetId()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// WatchTodo is a contract stream (path 1b): one implementation serves both gRPC streaming and HTTP SSE (mapped by
// the generated Kratos v3 stubs). The middleware chain runs once when the stream opens and wraps the whole stream;
// on the gRPC side there is no request message when the stream opens, so the validate middleware takes effect only
// over HTTP. The handler runs the proto's own rules with protovalidate (out of range is rejected, not clamped), so the
// limit is written once, in the proto.
// The demo pushes the current snapshot and ends; in real use, subscribe to an event channel and keep calling Send
// until ctx is canceled.
func (s *TodoService) WatchTodo(req *v1.WatchTodoRequest, stream v1.TodoService_WatchTodoServer) error {
	if err := protovalidate.Validate(req); err != nil {
		return biz.ErrInvalidArgument
	}
	todos, _, err := s.uc.ListTodo(stream.Context(), biz.ListOptions{Limit: int(req.GetLimit())})
	if err != nil {
		return err
	}
	for _, todo := range todos {
		if err := stream.Send(toProtoTodo(todo)); err != nil {
			return err
		}
	}
	return nil
}

// StreamTodoLookup is a contract bidi stream (path 1c): gRPC bidi streaming and HTTP WebSocket share this
// implementation (mapped by the generated Kratos v3 stubs). The middleware chain runs once when the stream opens and
// wraps the whole stream; there is no request message at that point, so validate takes effect on neither side, and
// per-message validation belongs in the loop below.
// The client streams ids and gets the matching Todo for each; it ends on io.EOF (client closed its send side).
func (s *TodoService) StreamTodoLookup(stream v1.TodoService_StreamTodoLookupServer) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		todo, err := s.uc.GetTodo(stream.Context(), req.GetId())
		if err != nil {
			return err
		}
		if err := stream.Send(toProtoTodo(todo)); err != nil {
			return err
		}
	}
}

// ExportTodo is a contract binary response (1d): a unary call returns HttpBody with the CSV as is, running through
// the middleware chain as usual. Large files and resumable downloads use the raw download path (see files.go).
func (s *TodoService) ExportTodo(ctx context.Context, req *v1.ExportTodoRequest) (*httpbody.HttpBody, error) {
	// limit passes through unchanged: biz fills the default for 0, and the transport rejects values above the proto lte.
	todos, err := s.uc.ExportTodo(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, err
	}

	// Use encoding/csv instead of building it by hand: %q produces Go string-literal escapes (\" and \n), while CSV
	// doubles quotes and quotes the whole field. With a quote or newline in a title, the former silently produces
	// broken data.
	var b bytes.Buffer
	cw := csv.NewWriter(&b)
	if err := cw.Write([]string{"id", "title", "completed"}); err != nil {
		return nil, err
	}
	for _, t := range todos {
		row := []string{strconv.FormatInt(t.ID, 10), t.Title, strconv.FormatBool(t.Completed)}
		if err := cw.Write(row); err != nil {
			return nil, err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, err
	}
	return &httpbody.HttpBody{ContentType: "text/csv; charset=utf-8", Data: b.Bytes()}, nil
}

// toBizCompleted converts the tri-state filter (0 all, 1 yes, 2 no) into the optional bool of biz. The transport's
// proto validation already rejects out-of-range values, so one reaching here can only come from an in-process call.
func toBizCompleted(v int32) (*bool, error) {
	switch v {
	case 0:
		return nil, nil
	case 1:
		return new(true), nil
	case 2:
		return new(false), nil
	}
	return nil, biz.ErrInvalidArgument
}

func toBizTodoSort(sort v1.ListTodoRequest_Sort) (biz.TodoSort, error) {
	switch sort {
	case v1.ListTodoRequest_SORT_UNSPECIFIED:
		return biz.TodoSortDefault, nil
	case v1.ListTodoRequest_SORT_UPDATE_TIME_DESC:
		return biz.TodoSortUpdateTimeDesc, nil
	}
	return "", biz.ErrInvalidArgument
}

func toProtoTodo(t *biz.Todo) *v1.Todo {
	if t == nil {
		return nil
	}
	return &v1.Todo{
		Id:         t.ID,
		Title:      t.Title,
		Content:    t.Content,
		Completed:  t.Completed,
		CreateTime: timestamppb.New(t.CreateTime),
		UpdateTime: timestamppb.New(t.UpdateTime),
	}
}

func toProtoTodoListItem(t *biz.Todo) *v1.TodoListItem {
	return &v1.TodoListItem{
		Id:         t.ID,
		Title:      t.Title,
		Completed:  t.Completed,
		CreateTime: timestamppb.New(t.CreateTime),
		UpdateTime: timestamppb.New(t.UpdateTime),
	}
}
