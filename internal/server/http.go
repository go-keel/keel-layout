package server

import (
	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/server/api"
)

func NewHTTPServer(todo *service.TodoService) api.HTTPServerRegister {
	return func(server *api.HTTPServer) {
		// Path 1: proto contract endpoints (registered by codegen, unary middleware chain, protojson responses)
		v1.RegisterTodoServiceHTTPServer(server, todo)
	}
}

// NewStreamRegister registers raw routes outside the proto (raw routes bypass the middleware chain).
// Every route must choose explicitly: sc.RequireLogin() requires login; sc.Anonymous() does not, but still resolves
// the identity when credentials are present; routes that skip framework authentication entirely (such as a webhook
// with its own signature check) use sc.Server(). There is no way to stay silent: without one of the three calls
// there is no register method, so it does not compile.
func NewStreamRegister(streamSvc *service.StreamService, webhook *service.WebhookHandler) api.StreamRegister {
	return func(sc *api.StreamContext) {
		// Example: delete after forking (through "Example end")
		// All examples sit under the /examples/ prefix and stay out of the business namespace.
		// 2a raw SSE: a stream of Todo snapshots; the example is anonymous (use sc.RequireLogin() to require login)
		sc.Anonymous().HandleFunc("/examples/sse", streamSvc.TodosSSE())
		// 2b raw WebSocket: free-form frames (for a contract bidi stream use a proto bidi RPC, see StreamTodoLookup)
		sc.Anonymous().HandleFunc("/examples/ws", service.WSEchoHandler())
		// 2c raw download: Range/resumable download (for a contract small-file export see ExportTodo)
		sc.Anonymous().HandleFunc("/examples/download", service.DemoDownloadHandler())
		// 2d webhook: callback from an external system; the signature is the credential, no framework authentication
		sc.Server().HandleFunc("/examples/webhook", webhook.HandleExample)

		// 2e pre-routing filter: verifies the signature of requests under /examples/signed/ before routing and request
		// decoding, and passes everything else through. In real use, set the prefix to the contract endpoints that need
		// signing: a contract endpoint decodes before entering the middleware chain, so the chain never sees the raw
		// bytes and the check can only happen here.
		sc.Filter(webhook.SignatureFilter("/examples/signed/"))
		// The filter above has verified the signature, so this route handles only the business; it is Anonymous
		// because the signature is the credential and no login is required. It also shows reading the client IP
		// outside the chain.
		sc.Anonymous().HandleFunc("/examples/signed/echo", service.SignedEchoHandler())
		// Example end
	}
}
