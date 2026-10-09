// Package webhookfx binds webhook ingress to Balda application policy.
package webhookfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
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

type ingressParams struct {
	fx.In

	Config   webhook.Config
	Service  *webhookapp.Service
	Provider state.Provider `optional:"true"`
}

func newIngress(params ingressParams) (*webhookapp.Ingress, error) {
	var store webhookapp.RouteStore
	if params.Provider != nil {
		store = routeLookup{store: params.Provider.WebhookRoutes()}
	}
	configured := make([]webhookapp.ConfiguredRoute, 0, len(params.Config.Routes))
	for name, raw := range params.Config.Routes {
		path := strings.TrimSpace(raw.Path)
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		var reportKind, reportKey string
		if raw.Envelope.ReportTo != nil {
			reportKind, reportKey = raw.Envelope.ReportTo.Target, raw.Envelope.ReportTo.Key
		}
		authType := strings.ToLower(strings.TrimSpace(raw.Auth.Type))
		if authType == "" {
			authType = webhookroutecmd.AuthTypeNone
		}
		dedupeSource := strings.ToLower(strings.TrimSpace(raw.Dedupe.Source))
		if dedupeSource == "" && strings.TrimSpace(raw.Dedupe.Header) != "" {
			dedupeSource = webhookroutecmd.DedupeSourceHeader
		}
		if dedupeSource == "" {
			dedupeSource = webhookroutecmd.DedupeSourceRequestID
		}
		configured = append(configured, webhookapp.ConfiguredRoute{
			Name: strings.TrimSpace(name), Path: path,
			Disabled:       !params.Config.Enabled,
			PromptTemplate: strings.TrimSpace(raw.PromptTemplate),
			ReportToKind:   reportKind, ReportToKey: reportKey,
			AckOnDelivery: raw.Envelope.AckOnDelivery,
			AuthType:      authType, AuthHeader: strings.TrimSpace(raw.Auth.Header),
			AuthValue:    strings.TrimSpace(raw.Auth.Value),
			DedupeSource: dedupeSource, DedupeHeader: strings.TrimSpace(raw.Dedupe.Header),
		})
	}
	return webhookapp.NewIngress(configured, store, params.Service)
}

type routeLookup struct{ store state.WebhookRouteStore }

func (l routeLookup) LookupByPath(ctx context.Context, path string) (webhookroutecmd.Record, bool, error) {
	r, found, err := l.store.LookupByPath(ctx, path)
	return routeRecord(r), found, err
}

func (l routeLookup) Get(ctx context.Context, name string) (webhookroutecmd.Record, bool, error) {
	r, found, err := l.store.Get(ctx, name)
	return routeRecord(r), found, err
}

func routeRecord(r state.WebhookRouteRecord) webhookroutecmd.Record {
	return webhookroutecmd.Record{Name: r.Name, Source: r.Source, Path: r.Path,
		PromptTemplate: r.PromptTemplate, ReportToKind: r.ReportToKind,
		ReportToKey: r.ReportToKey, AckOnDelivery: r.AckOnDelivery,
		DedupeSource: r.DedupeSource, DedupeHeader: r.DedupeHeader,
		AuthType: r.AuthType, AuthHeader: r.AuthHeader,
		SecretVerifier: r.SecretVerifier, Enabled: r.Enabled, Deleted: r.Deleted,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type receiverParams struct {
	fx.In

	Config   webhook.Config
	Ingress  webhook.Ingress
	Logger   zerolog.Logger
	Receipts webhook.DeliveryReceipts `optional:"true"`
}

func newReceiver(params receiverParams) (*webhook.Receiver, error) {
	receiver, err := webhook.NewReceiver(params.Config, params.Ingress, params.Logger)
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
		newIngress,
		fx.Annotate(func(ingress *webhookapp.Ingress) webhook.Ingress { return ingress }),
		newReceiver,
	),
	fx.Invoke(func(*webhook.Receiver) {}),
)
