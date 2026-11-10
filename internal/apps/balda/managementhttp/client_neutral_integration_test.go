package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/baldaworks/balda/internal/managementapi/v1/clienttest"
)

func TestClientNeutralGeneratedManagementFlow(t *testing.T) {
	aliases := &aliasFixture{}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, aliases, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}

	observation, err := clienttest.Exercise(t.Context(), client, clienttest.Input{
		Username:       browserAuthTestUsername,
		Password:       browserAuthTestPassword,
		BrowserBinding: strings.Repeat("b", 32),
		CSRFToken:      strings.Repeat("c", 32),
		AliasName:      "neutral-client",
		LocatorRef:     "telegram:123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Compatibility.ContractVersion == "" || observation.Principal.Username != browserAuthTestUsername {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.Created.Name != "neutral-client" || aliases.authority.UserID != browserAuthTestUsername || aliases.authority.WebAuthSessionID == "" {
		t.Fatalf("created = %#v, authority = %#v", observation.Created, aliases.authority)
	}
}

func TestClientNeutralAuthorityFailuresDoNotMutate(t *testing.T) {
	aliases := &countingAliasFixture{}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, aliases, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	principal, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(credentials.AccessToken, ""))
	if err != nil || principal.JSON200 == nil || principal.JSON200.WebAuthSessionID == "" {
		t.Fatalf("principal = %#v, err = %v", principal, err)
	}

	for _, test := range []struct {
		name   string
		editor managementv1.RequestEditorFn
		want   int
	}{
		{name: "missing access", editor: credentialRequestEditor("", credentials.CSRFToken), want: http.StatusUnauthorized},
		{name: "missing csrf", editor: credentialRequestEditor(credentials.AccessToken, ""), want: http.StatusForbidden},
		{name: "AI session ID", editor: credentialRequestEditor("ai-session-synthetic", credentials.CSRFToken), want: http.StatusUnauthorized},
		{name: "WebAuth session ID", editor: credentialRequestEditor(principal.JSON200.WebAuthSessionID, credentials.CSRFToken), want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := client.CreateAliasWithResponse(t.Context(), managementv1.AliasCreateRequest{
				Name: "must-not-exist", LocatorRef: "telegram:123",
			}, test.editor)
			if err != nil || response == nil {
				t.Fatalf("response = %#v, err = %v", response, err)
			}
			if response.StatusCode() != test.want {
				t.Fatalf("status = %d, want %d", response.StatusCode(), test.want)
			}
		})
	}
	if aliases.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", aliases.createCalls)
	}
}

type countingAliasFixture struct {
	createCalls int
}

func (*countingAliasFixture) List(context.Context, aliascmd.Authority) ([]aliascmd.Record, error) {
	return nil, nil
}
func (*countingAliasFixture) Get(context.Context, string, aliascmd.Authority) (aliascmd.Record, error) {
	return aliascmd.Record{}, nil
}
func (f *countingAliasFixture) Create(context.Context, aliascmd.Create) (aliascmd.Record, error) {
	f.createCalls++
	return aliascmd.Record{}, nil
}
func (*countingAliasFixture) Retarget(context.Context, aliascmd.Retarget) (aliascmd.Record, error) {
	return aliascmd.Record{}, nil
}
func (*countingAliasFixture) Delete(context.Context, aliascmd.Delete) error { return nil }
