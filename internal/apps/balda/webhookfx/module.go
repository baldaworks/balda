// Package webhookfx binds webhook ingress to Balda application policy.
package webhookfx

import (
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type serviceParams struct {
	fx.In

	Resolver   envelopetarget.DestinationResolver `optional:"true"`
	OwnerStore *auth.OwnerStore                   `optional:"true"`
	Admissions webhookapp.AdmissionStore          `optional:"true"`
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
	return webhookapp.NewService(targetResolver, params.Admissions, params.JobPub)
}

type receiverParams struct {
	fx.In

	Config   webhook.Config
	Service  webhook.Service
	Logger   zerolog.Logger
	Receipts webhook.DeliveryReceipts `optional:"true"`
}

func newReceiver(params receiverParams) (*webhook.Receiver, error) {
	receiver, err := webhook.NewReceiver(params.Config, params.Service, params.Logger)
	if err != nil {
		return nil, err
	}
	receiver.SetDeliveryReceipts(params.Receipts)
	if params.Config.Enabled {
		for _, route := range params.Config.Routes {
			if route.Envelope.AckOnDelivery && params.Receipts == nil {
				return nil, fmt.Errorf("webhook route requires a delivery receipt store")
			}
		}
	}
	return receiver, nil
}

// Module provides the inbound receiver and application policy at the host seam.
var Module = fx.Module("balda_webhook",
	fx.Provide(
		newWebhookappService,
		fx.Annotate(func(s *webhookapp.Service) webhook.Service { return s }),
		newReceiver,
	),
	fx.Invoke(func(*webhook.Receiver) {}),
)
