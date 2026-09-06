package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
)

type testTelegramAdapter struct {
	*baldatelegram.Adapter
}

func (*testTelegramAdapter) SupportedCommands() []string {
	return []string{"start", "help", "topic", "goalkeeper", "reset", "locator", "close", "cancel", "usage", "auto", "user", "plugin"}
}

func newTestTelegramAdapter(tgClient client.ClientWithResponsesInterface, formattingMode string) *testTelegramAdapter {
	msg := baldatelegram.NewMessenger(tgClient, zerolog.Nop())
	if strings.TrimSpace(formattingMode) != "" {
		msg.SetAgentReplyFormattingMode(formattingMode)
	}
	return &testTelegramAdapter{Adapter: baldatelegram.NewAdapter(baldatelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})}
}

func (a *testTelegramAdapter) CommandContextFromEvent(event *events.CommandEvent) (CommandContext, bool) {
	command, ok := a.Adapter.CommandContextFromEvent(event)
	if !ok {
		return CommandContext{}, false
	}
	return CommandContext(command), true
}

type testDeliveryAdapter interface {
	SendAgentReplyWithProviderMessageIDAndFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) (string, error)
	SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error
	SendMarkdownWithFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) error
	SendDraftPlain(ctx context.Context, locator deliverycmd.Locator, draftID int, text string) error
	SendTyping(ctx context.Context, locator deliverycmd.Locator) error
	SendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error
}

func handleDeliveryCommandForTest(ctx context.Context, adapter testDeliveryAdapter, env actorlayer.Envelope) error {
	if adapter == nil {
		return fmt.Errorf("delivery adapter is required")
	}
	var payload actors.DeliveryPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return err
	}
	switch payload.Mode {
	case actors.DeliveryModeAgentReply:
		_, err := adapter.SendAgentReplyWithProviderMessageIDAndFormat(ctx, payload.Locator, payload.DeliveryFormat, payload.Text)
		return err
	case actors.DeliveryModePlain:
		return adapter.SendPlain(ctx, payload.Locator, payload.Text)
	case actors.DeliveryModeMarkdown:
		return adapter.SendMarkdownWithFormat(ctx, payload.Locator, payload.DeliveryFormat, payload.Text)
	case actors.DeliveryModeDraftPlain:
		return adapter.SendDraftPlain(ctx, payload.Locator, payload.DraftID, payload.Text)
	case actors.DeliveryModeChatAction:
		return adapter.SendTyping(ctx, payload.Locator)
	case actors.DeliveryModeProgress:
		if payload.Progress == nil {
			return fmt.Errorf("progress payload is required")
		}
		return adapter.SendProgress(ctx, payload.Locator, *payload.Progress)
	default:
		return fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
}
