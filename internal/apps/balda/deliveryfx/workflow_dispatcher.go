package deliveryfx

import (
	"context"
	"fmt"

	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/deliveryworkflow"
)

type channelRouterDispatcher struct {
	router *baldachannel.Router
}

var _ deliveryworkflow.Dispatcher = channelRouterDispatcher{}

func NewChannelDispatcher(router *baldachannel.Router) deliveryworkflow.Dispatcher {
	return channelRouterDispatcher{router: router}
}

func (d channelRouterDispatcher) Dispatch(ctx context.Context, delivery deliveryworkflow.Delivery) (string, error) {
	if d.router == nil {
		return "", fmt.Errorf("channel router is required")
	}
	payload := applyFormattedMessage(delivery.Payload, delivery.Message)
	switch payload.Mode {
	case deliverycmd.ModeAgentReply:
		return d.router.SendAgentReplyWithQuestion(ctx, payload.Locator, payload.DeliveryFormat, payload.Text, payload.Question)
	case deliverycmd.ModePlain:
		return "", d.router.SendPlain(ctx, payload.Locator, payload.Text)
	case deliverycmd.ModeMarkdown:
		return "", d.router.SendMarkdownWithFormat(ctx, payload.Locator, payload.DeliveryFormat, payload.Text)
	case deliverycmd.ModeDraftPlain:
		return "", d.router.SendDraftPlain(ctx, payload.Locator, payload.DraftID, payload.Text)
	case deliverycmd.ModeChatAction:
		return "", d.router.SendTyping(ctx, payload.Locator)
	case deliverycmd.ModeProgress:
		if payload.Progress == nil {
			return "", fmt.Errorf("progress payload is required")
		}
		return "", d.router.SendProgress(ctx, payload.Locator, *payload.Progress)
	case deliverycmd.ModeClearQuestionControls:
		return "", d.router.SettleQuestionControls(ctx, payload.Locator, payload.MessageID, payload.Handle, payload.Text)
	case deliverycmd.ModePhoto:
		if payload.Media == nil {
			return "", fmt.Errorf("photo payload is required")
		}
		return "", d.router.SendPhotoMedia(ctx, payload.Locator, *payload.Media)
	case deliverycmd.ModeDocument:
		if payload.Media == nil {
			return "", fmt.Errorf("document payload is required")
		}
		return "", d.router.SendDocumentMedia(ctx, payload.Locator, *payload.Media)
	default:
		return "", fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
}

func applyFormattedMessage(payload deliverycmd.Payload, message *deliveryfmt.Message) deliverycmd.Payload {
	if message == nil {
		return payload
	}
	switch payload.Mode {
	case deliverycmd.ModeAgentReply, deliverycmd.ModeMarkdown:
		payload.Text = message.Text
	case deliverycmd.ModeProgress:
		if payload.Progress != nil {
			progress := *payload.Progress
			progress.Text = message.Text
			payload.Progress = &progress
		}
	case deliverycmd.ModePhoto, deliverycmd.ModeDocument:
		if payload.Media != nil {
			media := *payload.Media
			media.Caption = message.Text
			payload.Media = &media
		}
	}
	return payload
}
