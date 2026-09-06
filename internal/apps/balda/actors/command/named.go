package command

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
)

type HandlerFunc struct {
	CommandName string
	Run         func(context.Context, actorlayer.Envelope, commandcmd.Payload) error
}

func (h HandlerFunc) Name() string { return h.CommandName }
func (h HandlerFunc) Handle(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload) error {
	return h.Run(ctx, env, payload)
}
