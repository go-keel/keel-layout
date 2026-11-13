package biz

import "errors"

var (
	ErrUnexpectedError = errors.New("unexpected error")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrTodoNotFound    = errors.New("todo not found") // Example: delete after forking
)
