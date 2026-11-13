package service

import (
	"context"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	v1 "github.com/go-keel/keel-layout/api/keel_layout/v1"
	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/i18n"
	"github.com/go-keel/keel/middleware/sentinel"
)

func NewErrorSentinel() *sentinel.Group {
	return sentinel.NewGroup().
		OnLocaled(biz.ErrTodoNotFound, translateTodoNotFound). // Example: delete after forking
		OnLocaled(biz.ErrUnexpectedError, translateUnexpectedError).
		OnLocaled(biz.ErrInvalidArgument, translateInvalidArgument)
}

func translateUnexpectedError(_ context.Context, localizer *goi18n.Localizer, _ error) error {
	msg := i18n.MustLocalizeMessage(localizer, &goi18n.Message{
		ID:    "ErrUnexpectedError",
		Other: "Something went wrong. Please try again later.",
	})
	return v1.ErrorUnexpectedError("%s", msg)
}

func translateInvalidArgument(_ context.Context, localizer *goi18n.Localizer, _ error) error {
	msg := i18n.MustLocalizeMessage(localizer, &goi18n.Message{
		ID:    "ErrInvalidArgument",
		Other: "Invalid request. Please check your input.",
	})
	return v1.ErrorInvalidArgument("%s", msg)
}

// Example: delete after forking
func translateTodoNotFound(_ context.Context, localizer *goi18n.Localizer, _ error) error {
	msg := i18n.MustLocalizeMessage(localizer, &goi18n.Message{
		ID:    "ErrTodoNotFound",
		Other: "Todo not found.",
	})
	return v1.ErrorTodoNotFound("%s", msg)
}
