package server

import (
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/core/knats"
)

// NewConsumerServer registers every consumer and returns a ConsumerSetup that di.go passes in once when it builds
// the consumer server (knats.NewConsumerServer). This function grows with the business; the assembly seam keeps
// only the one constant line.
//
// With no consumer left, keep this function and stop building the consumer server in cmd/worker/di.go: a consumer
// server with no subscriptions fails startup (ErrNoConsumers).
func NewConsumerServer(
	handler *service.TodoCreatedConsumer, // Example: delete after forking
) knats.ConsumerSetup {
	return func(r knats.ConsumerRegistrar) {
		// Example: delete after forking (through "Example end")
		biz.EventTodoCreated.Handle(r, handler.Handler, knats.EventOptions{
			Durable: "todo_created",
			Workers: 3,
		})
		// Example end
	}
}
