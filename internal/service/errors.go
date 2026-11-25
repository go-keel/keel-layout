package service

import (
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/middleware/sentinel"
)

func NewErrorSentinel() *sentinel.Group {
	return sentinel.NewGroup().
		OnMessage(biz.ErrTodoNotFound, &goi18n.Message{ID: "ErrTodoNotFound", Other: "Todo not found."}, v1.ErrorTodoNotFound). // Example: delete after forking
		OnMessage(biz.ErrUnexpectedError, &goi18n.Message{ID: "ErrUnexpectedError", Other: "Something went wrong. Please try again later."}, v1.ErrorUnexpectedError).
		OnMessage(biz.ErrInvalidArgument, &goi18n.Message{ID: "ErrInvalidArgument", Other: "Invalid request. Please check your input."}, v1.ErrorInvalidArgument)
}
