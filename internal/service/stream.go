// Example file: delete the whole file after forking

package service

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/server/api"
)

// StreamService shows the SSE path: it works on http.ResponseWriter directly (Flush is available) and bypasses the
// unary middleware chain; to require login, the mounting code registers it with sc.RequireLogin() instead
// (see internal/server/http.go).
type StreamService struct {
	uc *biz.TodoUseCase
}

func NewStreamService(uc *biz.TodoUseCase) *StreamService {
	return &StreamService{uc: uc}
}

// TodosSSE returns the handler of GET /examples/sse:
// it pushes the current Todo snapshot (one SSE event per item) and ends.
// In real use, subscribe to an event channel and keep pushing until ctx.Done().
func (s *StreamService) TodosSSE() http.HandlerFunc {
	sse := api.SSEHandler(func(ctx context.Context, w *api.SSEWriter) error {
		todos, _, err := s.uc.ListTodo(ctx, biz.ListOptions{Limit: 100})
		if err != nil {
			return err
		}
		for _, todo := range todos {
			data, err := json.Marshal(todo)
			if err != nil {
				return err
			}
			if err := w.WriteEvent(api.SSEEvent{
				Event: "todo",
				Data:  string(data),
			}); err != nil {
				return err
			}
		}
		return nil // returning ends the stream; to keep pushing, watch ctx.Done() and subscribe to a business channel
	})

	// Raw routes skip codegen; without a method check, POST/PUT could get the event stream too.
	// SSE is GET only (EventSource only sends GET); HEAD is meaningless too, since a stream has no length.
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sse(w, r)
	}
}
