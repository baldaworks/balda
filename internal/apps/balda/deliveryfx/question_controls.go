package deliveryfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type questionControlPublisher struct {
	dispatcher actortransport.Dispatcher
}

// NewQuestionControlPublisher projects cleanup through durable command admission.
func NewQuestionControlPublisher(dispatcher actortransport.Dispatcher) questions.ControlPublisher {
	return questionControlPublisher{dispatcher: dispatcher}
}

func (p questionControlPublisher) ClearQuestionControls(ctx context.Context, request questions.ControlClearRequest) error {
	// Controls are currently projected only by Telegram. Keeping this decision
	// in composition avoids leaking channel capabilities into question lifecycle.
	if !strings.EqualFold(strings.TrimSpace(request.Locator.ChannelType), string(deliverycmd.ChannelTypeTelegram)) {
		return nil
	}
	env, err := deliverycmd.SettleQuestionControlsEnvelope(
		actorlayer.SystemAddress("question"),
		request.Locator,
		request.QuestionID,
		request.ProviderMessageID,
		request.ControlHandle,
		request.SelectionText,
	)
	if err != nil {
		return err
	}
	receipt, err := p.dispatcher.Dispatch(ctx, env)
	if err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("question control admission returned no receipt")
	}
	return nil
}
