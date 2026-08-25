package handlers

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/tgbotkit/runtime/events"
)

type CommandContext struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	ChatID          int64
	TopicID         int
	UserID          int64
	Command         string
	Args            string
	IsDM            bool
}

type CommandChannel interface {
	CommandContextFromEvent(event *events.CommandEvent) (CommandContext, bool)
	CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error)
	Close(ctx context.Context, locator deliverycmd.Locator) error
}

type CommandRegistry interface {
	OnCommand(handler func(context.Context, *events.CommandEvent) error)
}
