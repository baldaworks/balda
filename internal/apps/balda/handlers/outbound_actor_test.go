package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/actors"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/normahq/balda/pkg/actorlayer"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

func newTestTelegramAdapter(tgClient client.ClientWithResponsesInterface, formattingMode string) *baldatelegram.Adapter {
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	if strings.TrimSpace(formattingMode) != "" {
		msg.SetAgentReplyFormattingMode(formattingMode)
	}
	return baldatelegram.NewAdapter(baldatelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
}

func handleDeliveryCommandForTest(ctx context.Context, adapter *baldatelegram.Adapter, env actorlayer.Envelope) error {
	if adapter == nil {
		return fmt.Errorf("delivery adapter is required")
	}
	var payload actors.DeliveryPayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return err
	}
	switch payload.Mode {
	case actors.DeliveryModeAgentReply:
		_, err := adapter.SendAgentReplyWithProviderMessageIDAndProfile(ctx, payload.Locator, payload.Profile, payload.Text)
		return err
	case actors.DeliveryModePlain:
		return adapter.SendPlain(ctx, payload.Locator, payload.Text)
	case actors.DeliveryModeMarkdown:
		return adapter.SendMarkdownWithProfile(ctx, payload.Locator, payload.Profile, payload.Text)
	case actors.DeliveryModeDraftPlain:
		return adapter.SendDraftPlain(ctx, payload.Locator, payload.DraftID, payload.Text)
	case actors.DeliveryModeChatAction:
		return adapter.SendTyping(ctx, payload.Locator)
	default:
		return fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
}
