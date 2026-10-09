package webhookroutefx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type fakeStore struct {
	record   state.WebhookRouteRecord
	mutation state.WebhookRouteMutation
}

func (s *fakeStore) Get(context.Context, string) (state.WebhookRouteRecord, bool, error) {
	return s.record, true, nil
}

func (s *fakeStore) List(context.Context) ([]state.WebhookRouteRecord, error) {
	return []state.WebhookRouteRecord{s.record}, nil
}

func (s *fakeStore) LookupByPath(context.Context, string) (state.WebhookRouteRecord, bool, error) {
	return state.WebhookRouteRecord{}, false, nil
}

func (s *fakeStore) ReconcileConfig(context.Context, []state.WebhookRouteRecord) error { return nil }

func (s *fakeStore) CheckAuthority(context.Context, state.WebhookRouteAuthority) error { return nil }

func (s *fakeStore) Save(_ context.Context, mutation state.WebhookRouteMutation) error {
	s.mutation = mutation
	return nil
}

func TestRouteStorePreservesGuardedMutation(t *testing.T) {
	at := time.Now().UTC()
	store := &fakeStore{}
	adapter := &routeStore{store: store}
	record := webhookroutecmd.Record{Name: "events", Source: webhookroutecmd.SourceManaged,
		Path: "/events", PromptTemplate: "{{.RawBody}}", ReportToKind: "managed_alias",
		ReportToKey: "main_chat", DedupeSource: "body_sha256", AuthType: "header",
		AuthHeader: webhookroutecmd.ManagedSecretHeader, SecretVerifier: "verifier",
		Enabled: true, Version: 2, CreatedAt: at, UpdatedAt: at}
	authority := webhookroutecmd.Authority{UserID: "admin", SessionID: "browser",
		UserVersion: 1, CredentialVersion: 2, MFAVersion: 3, SessionVersion: 4, At: at}
	if err := adapter.Save(t.Context(), webhookroutecmd.Mutation{Kind: webhookroutecmd.MutationRotate,
		Record: record, ExpectedVersion: 1, Authority: authority}); err != nil {
		t.Fatal(err)
	}
	if got := store.mutation; got.Kind != state.WebhookRouteRotate ||
		got.Record.SecretVerifier != "verifier" || got.Record.ReportToKey != "main_chat" ||
		got.Authority.MFAVersion != 3 || got.ExpectedVersion != 1 {
		t.Fatalf("mapped mutation = %+v", got)
	}
	store.record = store.mutation.Record
	got, found, err := adapter.Get(t.Context(), "events")
	if err != nil || !found || got != record {
		t.Fatalf("mapped record = %+v, found=%t, err=%v", got, found, err)
	}
}

func TestRouteStoreErrorMapping(t *testing.T) {
	for _, tc := range []struct{ input, want error }{
		{state.ErrWebhookRouteInvalid, webhookroutecmd.ErrInvalid},
		{state.ErrWebhookRouteForbidden, webhookroutecmd.ErrForbidden},
		{state.ErrWebhookRouteNotFound, webhookroutecmd.ErrNotFound},
		{state.ErrWebhookRouteConflict, webhookroutecmd.ErrConflict},
		{state.ErrWebhookRouteUnavailable, webhookroutecmd.ErrUnavailable},
	} {
		if got := storeError(tc.input); !errors.Is(got, tc.want) {
			t.Errorf("storeError(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
