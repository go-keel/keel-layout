package server

import (
	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/service"
	"github.com/go-keel/keel/server/api"
)

func NewGRPCServer(todo *service.TodoService) api.GRPCServerRegister {
	return func(server *api.GRPCServer) {
		v1.RegisterTodoServiceServer(server, todo)
	}
}
