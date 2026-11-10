package managementhttp

import (
	"context"
	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGeneratedClientAliasLifecycleUsesCoreAuthority(t *testing.T) {
	fixture := &aliasFixture{}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, fixture))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	created, err := client.CreateAliasWithResponse(t.Context(), managementv1.AliasCreateRequest{Name: "ops", LocatorRef: "telegram:123"}, write)
	if err != nil || created.JSON200 == nil || fixture.authority.UserID != browserAuthTestUsername || fixture.authority.WebAuthSessionID == "" {
		t.Fatalf("create=%#v authority=%#v %v", created, fixture.authority, err)
	}
	inventory, err := client.ListAliasesWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Items) != 1 {
		t.Fatalf("inventory=%#v %v", inventory, err)
	}
	detail, err := client.GetAliasWithResponse(t.Context(), "ops", read)
	if err != nil || detail.JSON200 == nil {
		t.Fatalf("detail=%#v %v", detail, err)
	}
	withoutCSRF, err := client.RetargetAliasWithResponse(t.Context(), "ops", managementv1.AliasRetargetRequest{ExpectedVersion: 1, LocatorRef: "telegram:456"}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden {
		t.Fatalf("retarget without csrf=%#v %v", withoutCSRF, err)
	}
	retargeted, err := client.RetargetAliasWithResponse(t.Context(), "ops", managementv1.AliasRetargetRequest{ExpectedVersion: 1, LocatorRef: "telegram:456"}, write)
	if err != nil || retargeted.JSON200 == nil || retargeted.JSON200.Version != 2 {
		t.Fatalf("retarget=%#v %v", retargeted, err)
	}
	deleted, err := client.DeleteAliasWithResponse(t.Context(), "ops", managementv1.AliasDeleteRequest{ExpectedVersion: 2}, write)
	if err != nil || deleted.StatusCode() != http.StatusNoContent {
		t.Fatalf("delete=%#v %v", deleted, err)
	}
}

type aliasFixture struct {
	record    aliascmd.Record
	authority aliascmd.Authority
}

func (f *aliasFixture) List(context.Context, aliascmd.Authority) ([]aliascmd.Record, error) {
	return []aliascmd.Record{f.record}, nil
}
func (f *aliasFixture) Get(context.Context, string, aliascmd.Authority) (aliascmd.Record, error) {
	return f.record, nil
}
func (f *aliasFixture) Create(_ context.Context, r aliascmd.Create) (aliascmd.Record, error) {
	f.authority = r.Authority
	now := time.Now().UTC()
	f.record = aliascmd.Record{Name: r.Name, LocatorRef: r.LocatorRef, Version: 1, CreatedAt: now, UpdatedAt: now}
	return f.record, nil
}
func (f *aliasFixture) Retarget(_ context.Context, r aliascmd.Retarget) (aliascmd.Record, error) {
	f.authority = r.Authority
	f.record.LocatorRef = r.LocatorRef
	f.record.Version = 2
	f.record.UpdatedAt = time.Now().UTC()
	return f.record, nil
}
func (f *aliasFixture) Delete(_ context.Context, r aliascmd.Delete) error {
	f.authority = r.Authority
	return nil
}
