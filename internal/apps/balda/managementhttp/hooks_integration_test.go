package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientHookLifecycleUsesCoreAuthority(t *testing.T) {
	fixture := &hookFixture{record: hookcmd.Record{}}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, fixture, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	inventory, err := client.ListHooksWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || inventory.JSON200.Version != 0 || inventory.JSON200.Capabilities != "native adapters" {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	definition := managementv1.HookDefinition{Name: "prompt-check", Event: managementv1.UserPromptSubmit, Command: "/usr/local/bin/check", Args: []string{"--json"}, TimeoutSeconds: 10}
	withoutCSRF, err := client.CreateHookWithResponse(t.Context(), managementv1.HookSaveRequest{Definition: definition}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden || fixture.saveCalls != 0 {
		t.Fatalf("create without csrf = %#v, calls = %d, %v", withoutCSRF, fixture.saveCalls, err)
	}
	nullArgs := definition
	nullArgs.Args = nil
	invalid, err := client.CreateHookWithResponse(t.Context(), managementv1.HookSaveRequest{Definition: nullArgs}, write)
	if err != nil || invalid.StatusCode() != http.StatusBadRequest || fixture.saveCalls != 0 {
		t.Fatalf("create with null args = %#v, calls = %d, %v", invalid, fixture.saveCalls, err)
	}
	created, err := client.CreateHookWithResponse(t.Context(), managementv1.HookSaveRequest{Definition: definition}, write)
	if err != nil || created.StatusCode() != http.StatusNoContent || fixture.saveCalls != 1 || fixture.authority.UserID != browserAuthTestUsername || fixture.authority.WebAuthSessionID == "" {
		t.Fatalf("create = %#v, authority = %#v, calls = %d, %v", created, fixture.authority, fixture.saveCalls, err)
	}
	definition.Enabled = true
	mismatch, err := client.UpdateHookWithResponse(t.Context(), "other", managementv1.HookSaveRequest{Definition: definition, ExpectedVersion: 1}, write)
	if err != nil || mismatch.StatusCode() != http.StatusBadRequest || fixture.saveCalls != 1 {
		t.Fatalf("mismatched update = %#v, calls = %d, %v", mismatch, fixture.saveCalls, err)
	}
	updated, err := client.UpdateHookWithResponse(t.Context(), definition.Name, managementv1.HookSaveRequest{Definition: definition, ExpectedVersion: 1}, write)
	if err != nil || updated.StatusCode() != http.StatusNoContent || fixture.saveCalls != 2 || fixture.saved.Create {
		t.Fatalf("update = %#v, save = %#v, calls = %d, %v", updated, fixture.saved, fixture.saveCalls, err)
	}
	unconfirmed, err := client.DeleteHookWithResponse(t.Context(), definition.Name, managementv1.HookDeleteRequest{ExpectedVersion: 2}, write)
	if err != nil || unconfirmed.StatusCode() != http.StatusBadRequest || fixture.deleteCalls != 0 {
		t.Fatalf("unconfirmed delete = %#v, calls = %d, %v", unconfirmed, fixture.deleteCalls, err)
	}
	deleted, err := client.DeleteHookWithResponse(t.Context(), definition.Name, managementv1.HookDeleteRequest{ExpectedVersion: 2, Confirm: true}, write)
	if err != nil || deleted.StatusCode() != http.StatusNoContent || fixture.deleteCalls != 1 {
		t.Fatalf("delete = %#v, calls = %d, %v", deleted, fixture.deleteCalls, err)
	}
	confused, err := client.ListHooksWithResponse(t.Context(), credentialRequestEditor("same-raw-ai-session-id", ""))
	if err != nil || confused.StatusCode() != http.StatusUnauthorized || fixture.inventoryCalls != 1 {
		t.Fatalf("AI session credential = %#v, inventory calls = %d, %v", confused, fixture.inventoryCalls, err)
	}
}

type hookFixture struct {
	record         hookcmd.Record
	saved          hookcmd.Save
	authority      hookcmd.Authority
	saveCalls      int
	deleteCalls    int
	inventoryCalls int
}

func (*hookFixture) CapabilityDescription() string { return "native adapters" }

func (f *hookFixture) Inventory(_ context.Context, authority hookcmd.Authority) (hookcmd.Record, error) {
	f.inventoryCalls++
	f.authority = authority
	return f.record, nil
}

func (f *hookFixture) Save(_ context.Context, request hookcmd.Save) error {
	f.saveCalls++
	f.saved = request
	f.authority = request.Authority
	f.record.Version = request.ExpectedVersion + 1
	f.record.Definitions = []hookcmd.Definition{request.Definition}
	f.record.UpdatedAt = request.Authority.At.Format("2006-01-02T15:04:05.999999999Z07:00")
	return nil
}

func (f *hookFixture) Delete(_ context.Context, request hookcmd.Delete) error {
	f.deleteCalls++
	f.authority = request.Authority
	return nil
}
