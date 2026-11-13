// Example file: delete the whole file after forking

package service

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/go-keel/keel/server/api"
)

// WSEchoHandler shows the raw WebSocket path (path 3: bypasses the unary middleware chain), for free-form frames:
// text protocols, binary forwarding, non-proto message formats dictated by a third party.
// For a contract bidi stream prefer a proto bidi-stream RPC (see StreamTodoLookup, which runs the full chain).
func WSEchoHandler() http.HandlerFunc {
	return api.WebSocketHandler(func(ctx context.Context, conn *websocket.Conn) error {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return err // the helper classifies a client disconnect as a normal end
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return err
			}
		}
	}, api.WSOptions{ReadLimit: 1 << 20}) // 1 MiB per-frame limit
}
