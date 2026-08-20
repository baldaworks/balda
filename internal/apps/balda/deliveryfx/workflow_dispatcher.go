package deliveryfx

import (
	"context"
	"fmt"

	baldachannel "github.com/baldaworks/balda/internal/apps/balda/channel"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
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
	operation, err := channelOperation(delivery.Payload, delivery.Message)
	if err != nil {
		return "", err
	}
	result, err := d.router.Deliver(ctx, delivery.Payload.Locator, operation)
	return result.ProviderMessageID, err
}

func channelOperation(payload deliverycmd.Payload, message *deliveryfmt.Message) (deliverycmd.Operation, error) {
	operation := deliverycmd.Operation{
		DeliveryFormat: payload.DeliveryFormat,
		Message:        message,
		Text:           payload.Text,
		DraftID:        payload.DraftID,
		Question:       payload.Question,
		MessageID:      payload.MessageID,
		Handle:         payload.Handle,
	}
	switch payload.Mode {
	case deliverycmd.ModeAgentReply:
		operation.Kind = deliverycmd.OperationAgentReply
	case deliverycmd.ModePlain:
		operation.Kind = deliverycmd.OperationPlain
	case deliverycmd.ModeMarkdown:
		operation.Kind = deliverycmd.OperationMarkdown
	case deliverycmd.ModeDraftPlain:
		operation.Kind = deliverycmd.OperationDraft
	case deliverycmd.ModeChatAction:
		operation.Kind = deliverycmd.OperationTyping
	case deliverycmd.ModeProgress:
		if payload.Progress == nil {
			return deliverycmd.Operation{}, fmt.Errorf("progress payload is required")
		}
		operation.Kind = deliverycmd.OperationProgress
		operation.Progress = *payload.Progress
		if message != nil {
			operation.Progress.Text = message.Text
		}
	case deliverycmd.ModeClearQuestionControls:
		operation.Kind = deliverycmd.OperationClearQuestionControls
	case deliverycmd.ModePhoto, deliverycmd.ModeDocument:
		if payload.Media == nil {
			return deliverycmd.Operation{}, fmt.Errorf("%s payload is required", payload.Mode)
		}
		media := *payload.Media
		if message != nil {
			media.Caption = message.Text
		}
		operation.Media = &media
		if payload.Mode == deliverycmd.ModePhoto {
			operation.Kind = deliverycmd.OperationPhoto
		} else {
			operation.Kind = deliverycmd.OperationDocument
		}
	default:
		return deliverycmd.Operation{}, fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
	if message != nil && (payload.Mode == deliverycmd.ModeAgentReply || payload.Mode == deliverycmd.ModeMarkdown) {
		operation.Text = message.Text
	}
	return operation, nil
}
