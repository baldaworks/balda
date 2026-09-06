package command

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
)

// Actor validates durable ingress and delegates product behavior by command name.
type Actor struct {
	router *Router
}

func NewActor(router *Router) *Actor {
	return &Actor{router: router}
}
func (a *Actor) Address() string { return actorlayer.WildcardAddress(actorcmd.ActorTypeCommand) }
func (a *Actor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	payload, err := commandcmd.Decode(env)
	if err != nil {
		return actorlayer.PolicyError(fmt.Errorf("decode command envelope: %w", err))
	}
	handler, ok := a.router.Resolve(payload.Name)
	if !ok {
		return actorlayer.PolicyError(fmt.Errorf("unsupported command %q", payload.Name))
	}
	return handler.Handle(ctx, env, payload)
}
