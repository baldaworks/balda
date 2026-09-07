package webhook

import (
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type serviceParams struct {
	fx.In

	Resolver   envelopetarget.DestinationResolver `optional:"true"`
	OwnerStore *auth.OwnerStore                   `optional:"true"`
	SessionPub webhookapp.SessionPublisher        `optional:"true"`
	JobPub     webhookapp.JobPublisher            `optional:"true"`
}

func newWebhookappService(params serviceParams) *webhookapp.Service {
	resolver := params.Resolver
	if resolver == nil && params.OwnerStore != nil {
		resolver = params.OwnerStore
	}
	var targetResolver webhookapp.TargetResolver
	if resolver != nil {
		targetResolver = webhookapp.NewDestinationTargetResolver(resolver)
	}
	return webhookapp.NewService(targetResolver, params.SessionPub, params.JobPub)
}

type receiverParams struct {
	fx.In

	Config  Config
	Service *webhookapp.Service
	Logger  zerolog.Logger
}

func newReceiver(params receiverParams) (*Receiver, error) {
	return NewReceiver(params.Config, params.Service, params.Logger)
}

// Module provides the inbound webhook receiver and its application service.
var Module = fx.Module("balda_webhook_channel",
	fx.Provide(
		newWebhookappService,
		fx.Annotate(
			func(s *webhookapp.Service) Service { return s },
		),
		newReceiver,
	),
	fx.Invoke(
		func(*Receiver) {},
	),
)
