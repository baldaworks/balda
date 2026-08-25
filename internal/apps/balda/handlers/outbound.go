package handlers

import (
	"context"
	"fmt"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

var (
	commandHandlerActorAddress = actorlayer.ActorAddress{Target: "handler", Key: "command"}
	userHandlerActorAddress    = actorlayer.ActorAddress{Target: "handler", Key: "user"}
	startHandlerActorAddress   = actorlayer.ActorAddress{Target: "handler", Key: "start"}
)

func dispatchOutbound(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func sendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendMarkdown(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.MarkdownEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendAgentReply(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	return sendAgentReplyWithFormat(ctx, dispatcher, from, locator, "", text)
}

func sendAgentReplyWithFormat(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, format deliveryfmt.DeliveryFormat, text string) error {
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement("", from, locator, format, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}
