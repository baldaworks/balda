package command

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

var actorAddress = actorlayer.ActorAddress{Target: "command", Key: "handler"}

func dispatch(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope, operationID, suffix string) error {
	if dispatcher == nil {
		return fmt.Errorf("delivery dispatcher is required")
	}
	key := operationID + ":delivery:" + suffix
	env.ID, env.DedupeKey, env.CorrelationID, env.CausationID = key, key, operationID, operationID
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func SendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, operationID string, locator deliverycmd.Locator, text, suffix string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", actorAddress, locator, deliverycmd.SettlementBypass, text, suffix)
	if err != nil {
		return err
	}
	return dispatch(ctx, dispatcher, env, operationID, suffix)
}

func SendStructured(ctx context.Context, dispatcher actortransport.Dispatcher, operationID string, locator deliverycmd.Locator, p deliveryfmt.StructuredPresentation, suffix string) error {
	env, err := deliverycmd.MarkdownEnvelopeWithFormatAndSettlement("", actorAddress, locator, p.DeliveryFormat, deliverycmd.SettlementBypass, p.Text, suffix)
	if err != nil {
		return err
	}
	return dispatch(ctx, dispatcher, env, operationID, suffix)
}

func SendMarkdown(ctx context.Context, dispatcher actortransport.Dispatcher, operationID string, locator deliverycmd.Locator, text, suffix string) error {
	env, err := deliverycmd.MarkdownEnvelopeWithSettlement("", actorAddress, locator, deliverycmd.SettlementBypass, text, suffix)
	if err != nil {
		return err
	}
	return dispatch(ctx, dispatcher, env, operationID, suffix)
}
