// Package webhookroutefx binds webhook route policy to Balda state persistence.
package webhookroutefx

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type routeStore struct{ store state.WebhookRouteStore }

// NewStore adapts the host's durable route store to the policy-owned port.
func NewStore(provider state.Provider) *routeStore {
	return &routeStore{store: provider.WebhookRoutes()}
}

func (s *routeStore) Get(ctx context.Context, name string) (webhookroutecmd.Record, bool, error) {
	r, found, err := s.store.Get(ctx, name)
	return fromStateRecord(r), found, storeError(err)
}

func (s *routeStore) List(ctx context.Context) ([]webhookroutecmd.Record, error) {
	routes, err := s.store.List(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	items := make([]webhookroutecmd.Record, 0, len(routes))
	for _, r := range routes {
		items = append(items, fromStateRecord(r))
	}
	return items, nil
}

func (s *routeStore) ReconcileConfig(ctx context.Context, routes []webhookroutecmd.Record) error {
	records := make([]state.WebhookRouteRecord, 0, len(routes))
	for _, r := range routes {
		records = append(records, toStateRecord(r))
	}
	return storeError(s.store.ReconcileConfig(ctx, records))
}

func (s *routeStore) CheckAuthority(ctx context.Context, a webhookroutecmd.Authority) error {
	return storeError(s.store.CheckAuthority(ctx, stateAuthority(a)))
}

func (s *routeStore) Save(ctx context.Context, m webhookroutecmd.Mutation) error {
	return storeError(s.store.Save(ctx, state.WebhookRouteMutation{
		Kind: state.WebhookRouteMutationKind(m.Kind), Record: toStateRecord(m.Record),
		ExpectedVersion: m.ExpectedVersion, Authority: stateAuthority(m.Authority),
		Audit: m.Audit,
	}))
}

func fromStateRecord(r state.WebhookRouteRecord) webhookroutecmd.Record {
	return webhookroutecmd.Record{Name: r.Name, Source: r.Source, Path: r.Path,
		PromptTemplate: r.PromptTemplate, ReportToKind: r.ReportToKind,
		ReportToKey: r.ReportToKey, AckOnDelivery: r.AckOnDelivery,
		DedupeSource: r.DedupeSource, DedupeHeader: r.DedupeHeader,
		AuthType: r.AuthType, AuthHeader: r.AuthHeader,
		SecretVerifier: r.SecretVerifier, Enabled: r.Enabled, Deleted: r.Deleted,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func toStateRecord(r webhookroutecmd.Record) state.WebhookRouteRecord {
	return state.WebhookRouteRecord{Name: r.Name, Source: r.Source, Path: r.Path,
		PromptTemplate: r.PromptTemplate, ReportToKind: r.ReportToKind,
		ReportToKey: r.ReportToKey, AckOnDelivery: r.AckOnDelivery,
		DedupeSource: r.DedupeSource, DedupeHeader: r.DedupeHeader,
		AuthType: r.AuthType, AuthHeader: r.AuthHeader,
		SecretVerifier: r.SecretVerifier, Enabled: r.Enabled, Deleted: r.Deleted,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func stateAuthority(a webhookroutecmd.Authority) state.WebhookRouteAuthority {
	return state.WebhookRouteAuthority{UserID: a.UserID, UserVersion: a.UserVersion,
		CredentialVersion: a.CredentialVersion, MFAVersion: a.MFAVersion,
		SessionID: a.SessionID, SessionVersion: a.SessionVersion, At: a.At}
}

func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrWebhookRouteInvalid):
		return webhookroutecmd.ErrInvalid
	case errors.Is(err, state.ErrWebhookRouteForbidden):
		return webhookroutecmd.ErrForbidden
	case errors.Is(err, state.ErrWebhookRouteNotFound):
		return webhookroutecmd.ErrNotFound
	case errors.Is(err, state.ErrWebhookRouteConflict):
		return fmt.Errorf("%w: %v", webhookroutecmd.ErrConflict, err)
	default:
		return webhookroutecmd.ErrUnavailable
	}
}
