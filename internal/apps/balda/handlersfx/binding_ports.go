package handlersfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type bindingInvitationAdmitter interface {
	Consume(ctx context.Context, proof usercmd.BindingProof) (string, error)
}

type bindingChannelRegistry interface {
	Get(channel string) (usercmd.BindingChannel, bool)
	Register(info usercmd.BindingChannel) error
}
