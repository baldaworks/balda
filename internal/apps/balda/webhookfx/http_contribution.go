package webhookfx

import (
	"context"
	"fmt"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"go.uber.org/fx"
)

// HTTPContribution mounts the receiver with a live canonical route lookup.
type HTTPContribution func(context.Context, *httpfx.Registry) error

// NewHTTPContribution prepares the generic receiver for the shared listener.
func NewHTTPContribution(receiver *webhook.Receiver, ingress *webhookapp.Ingress) HTTPContribution {
	return func(_ context.Context, registry *httpfx.Registry) error {
		if receiver == nil || ingress == nil || registry == nil {
			return fmt.Errorf("generic webhook HTTP contribution requires receiver, ingress, and registry")
		}
		return registry.SetWebhookLookup("generic webhooks", receiver.Handler(), ingress.IsActivePath)
	}
}

type httpContributionParams struct {
	fx.In
	Receiver *webhook.Receiver
	Ingress  *webhookapp.Ingress
}

func newHTTPContribution(params httpContributionParams) HTTPContribution {
	return NewHTTPContribution(params.Receiver, params.Ingress)
}
