package webhookfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"go.uber.org/fx"
)

// ActiveRouteLister reads persisted route paths for startup conflict checks.
type ActiveRouteLister interface {
	List(ctx context.Context) ([]state.WebhookRouteRecord, error)
}

// HTTPContribution registers active generic webhook paths and a live exact-path lookup.
type HTTPContribution func(context.Context, *httpfx.Registry) error

// NewHTTPContribution prepares the generic receiver for the shared listener.
// Existing stored paths are registered verbatim; later managed edits use the live lookup.
func NewHTTPContribution(config webhook.Config, receiver *webhook.Receiver, ingress *webhookapp.Ingress, routes ActiveRouteLister) HTTPContribution {
	return func(ctx context.Context, registry *httpfx.Registry) error {
		if receiver == nil || ingress == nil || registry == nil {
			return fmt.Errorf("generic webhook HTTP contribution requires receiver, ingress, and registry")
		}
		handler := receiver.Handler()
		if err := registry.SetWebhookLookup("generic webhooks", handler, ingress.IsActivePath); err != nil {
			return err
		}
		if config.Enabled {
			for name, route := range config.Routes {
				path := strings.TrimSpace(route.Path)
				if path != "" && !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
				if err := registry.AddWebhook("config webhook "+strings.TrimSpace(name), path, handler); err != nil {
					return err
				}
			}
		}
		if routes == nil {
			return nil
		}
		stored, err := routes.List(ctx)
		if err != nil {
			return fmt.Errorf("list active webhook paths: %w", err)
		}
		for _, route := range stored {
			if route.Source != state.WebhookRouteSourceManaged || !route.Enabled || route.Deleted {
				continue
			}
			if err := registry.AddWebhook("managed webhook "+route.Name, route.Path, handler); err != nil {
				return err
			}
		}
		return nil
	}
}

type httpContributionParams struct {
	fx.In

	Config   webhook.Config
	Receiver *webhook.Receiver
	Ingress  *webhookapp.Ingress
	Provider state.Provider `optional:"true"`
}

func newHTTPContribution(params httpContributionParams) HTTPContribution {
	var routes ActiveRouteLister
	if params.Provider != nil {
		routes = params.Provider.WebhookRoutes()
	}
	return NewHTTPContribution(params.Config, params.Receiver, params.Ingress, routes)
}
