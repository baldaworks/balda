package mcpbackofficeapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

type authorizationTimestampStore struct {
	*mcpfx.DefinitionStore
	mutation mcpmanage.Mutation
}

func (s *authorizationTimestampStore) SaveDefinition(ctx context.Context, mutation mcpmanage.Mutation) error {
	if err := s.DefinitionStore.SaveDefinition(ctx, mutation); err != nil {
		return err
	}
	s.mutation = mutation
	return nil
}

func TestAuthorizationMetadataStaysMonotonicWithCurrentSecurityTime(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		name := "existing binding"
		if fresh {
			name = "fresh connection"
		}
		t.Run(name, func(t *testing.T) {
			operations, provider, catalog, credentials, authority := operationsFixture(t, nil)
			store := &authorizationTimestampStore{DefinitionStore: mcpfx.NewDefinitionStore(provider.MCP())}
			probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), nil)
			if err != nil {
				t.Fatal(err)
			}
			operations.definitions, err = mcpmanage.NewDefinitions(credentials, store, mcpfx.NewConfiguredDefinitions(nil, map[string]agentconfig.Config{"hosted": {}}, "hosted", nil), catalog, probe)
			if err != nil {
				t.Fatal(err)
			}
			const resource = "https://worker.example.test/mcp"
			floor := time.Now().UTC().Add(time.Hour)
			definition := mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: resource, OAuth: true, Scopes: []string{"tools"}, Targets: mcpcmd.Targets{All: true}}
			before := time.Now().UTC()
			var item mcpcmd.Item
			if fresh {
				request := mcpcmd.CreateDefinition{PublicID: "worker", Definition: definition, Authority: authority}
				request.Authority.At = floor
				item, err = operations.Create(t.Context(), request)
			} else {
				r, prepareErr := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "original-revision", Definition: definition, CreatedAt: floor}, mcpcmd.ValueEdits{})
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				c := mcpcmd.Connection{ID: r.ConnectionID, PublicID: "worker", Source: mcpcmd.SourceManaged, CurrentRevisionID: r.ID, CreatedAt: floor, UpdatedAt: floor.Add(time.Minute)}
				audit := fixtureAudit("clock-fixture", usercmd.AuditActionMCPDefinitionChanged, usercmd.AuditTargetMCP, c.ID)
				authority.At = audit.OccurredAt
				audit.ActorUserID, audit.ActorSessionID = authority.UserID, authority.SessionID
				if err := provider.MCP().SaveMCPConnection(t.Context(), state.MCPMutation{Connection: c, Revision: &r, Authority: authority, Audit: audit}); err != nil {
					t.Fatal(err)
				}
				floor = c.UpdatedAt
				oauth := &authorizationOAuth{resource: resource}
				grants, grantErr := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(provider.MCP()), oauth)
				if grantErr != nil {
					t.Fatal(grantErr)
				}
				authorizations, authErr := mcpmanage.NewAuthorizations(grants, "https://backoffice.example.test/mcp/oauth/callback", operations.definitions)
				if authErr != nil {
					t.Fatal(authErr)
				}
				defer authorizations.Close()
				if err := operations.ConfigureAuthorizations(authorizations); err != nil {
					t.Fatal(err)
				}
				started, beginErr := operations.BeginBrowser(t.Context(), mcpcmd.BeginAuthorization{ConnectionID: c.ID, ClientID: "worker-client", ClientAuthMethod: mcpcmd.ClientAuthNone, Authority: authority})
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				location, parseErr := url.Parse(started.AuthorizationURL)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				item, err = operations.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: location.Query().Get("state"), Code: "one-use-code", Issuer: resource, Authority: authority})
			}
			if err != nil || item.Connection.UpdatedAt.Before(floor) {
				t.Fatalf("metadata after wall rollback = %+v/%v", item.Connection, err)
			}
			mutation := store.mutation
			if mutation.Authority.At.Before(before) || mutation.Authority.At.After(time.Now().UTC()) || !mutation.Authority.At.Before(floor) || !mutation.Audit.OccurredAt.Equal(mutation.Authority.At) {
				t.Fatal("metadata floor changed security/audit time")
			}
		})
	}
}

type authorizationOAuth struct {
	mcpmanage.OAuthProvider
	resource  string
	exchanges int
	release   chan struct{}
}

func (o *authorizationOAuth) Discover(_ context.Context, resource, hint string) (mcpmanage.OAuthMetadata, error) {
	if resource != o.resource || hint != "" {
		return mcpmanage.OAuthMetadata{}, mcpcmd.ErrInvalid
	}
	return mcpmanage.OAuthMetadata{Resource: resource, Issuer: resource, TokenEndpoint: resource + "/token", AuthorizationEndpoint: resource + "/authorize", DeviceAuthorizationEndpoint: resource + "/device", GrantTypes: []string{"authorization_code", "urn:ietf:params:oauth:grant-type:device_code"}, ResponseTypes: []string{"code"}, PKCEMethods: []string{"S256"}, AuthMethods: []string{mcpcmd.ClientAuthNone}, Scopes: []string{"tools"}, RequireIssuerParameter: true}, nil
}

func (o *authorizationOAuth) BeginCode(_ mcpmanage.OAuthMetadata, _ mcpcmd.Grant, _, state string) (string, string, error) {
	return o.resource + "/authorize?state=" + url.QueryEscape(state), "private-verifier", nil
}

func (o *authorizationOAuth) ExchangeCode(context.Context, mcpmanage.OAuthMetadata, mcpcmd.Grant, mcpmanage.GrantSecrets, string, string, string) (mcpmanage.OAuthToken, error) {
	o.exchanges++
	return authorizationToken(), nil
}

func (o *authorizationOAuth) BeginDevice(context.Context, mcpmanage.OAuthMetadata, mcpcmd.Grant, mcpmanage.GrantSecrets) (mcpmanage.OAuthDevice, error) {
	return mcpmanage.OAuthDevice{Code: "private-device-code", UserCode: "ONCE-ONLY", VerificationURI: o.resource + "/verify", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (o *authorizationOAuth) PollDevice(ctx context.Context, _ mcpmanage.OAuthMetadata, _ mcpcmd.Grant, _ mcpmanage.GrantSecrets, _ mcpmanage.OAuthDevice) (mcpmanage.OAuthToken, error) {
	select {
	case <-o.release:
		return authorizationToken(), nil
	case <-ctx.Done():
		return mcpmanage.OAuthToken{}, ctx.Err()
	}
}

func authorizationToken() mcpmanage.OAuthToken {
	return mcpmanage.OAuthToken{Secrets: mcpmanage.GrantSecrets{AccessToken: "private-worker-access", TokenType: "Bearer"}, Scopes: []string{"tools"}, ExpiresAt: time.Now().Add(time.Hour)}
}

type authorizationBindingFunc func(context.Context, mcpcmd.SelectAuthorization) (mcpcmd.Item, error)

func (f authorizationBindingFunc) BindAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	return f(ctx, request)
}

func TestAuthorizationCompletionEditAfterSaveKeepsGrantIsolated(t *testing.T) {
	for _, device := range []bool{false, true} {
		name := "browser"
		if device {
			name = "device"
		}
		t.Run(name, func(t *testing.T) {
			operations, provider, _, credentials, authority := operationsFixture(t, nil)
			const resource = "https://worker.example.test/mcp"
			created, err := operations.Create(t.Context(), mcpcmd.CreateDefinition{PublicID: "worker", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: resource, OAuth: true, Scopes: []string{"tools"}, Targets: mcpcmd.Targets{All: true}}, Authority: authority})
			if err != nil {
				t.Fatal(err)
			}
			oauth := &authorizationOAuth{resource: resource, release: make(chan struct{})}
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(provider.MCP()), oauth)
			if err != nil {
				t.Fatal(err)
			}
			bound := make(chan error, 1)
			binder := authorizationBindingFunc(func(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
				d := created.Definition
				d.URL = resource + "/edited"
				if _, err := operations.Update(ctx, mcpcmd.UpdateDefinition{ConnectionID: created.Connection.ID, ExpectedVersion: created.Connection.Version, Definition: d, Authority: authority}); err != nil {
					bound <- err
					return mcpcmd.Item{}, err
				}
				item, err := operations.definitions.BindAuthorization(ctx, request)
				bound <- err
				return item, err
			})
			authorizations, err := mcpmanage.NewAuthorizations(grants, "https://backoffice.example.test/mcp/oauth/callback", binder)
			if err != nil {
				t.Fatal(err)
			}
			defer authorizations.Close()
			if err := operations.ConfigureAuthorizations(authorizations); err != nil {
				t.Fatal(err)
			}
			request := mcpcmd.BeginAuthorization{ConnectionID: created.Connection.ID, ClientID: "worker-client", ClientAuthMethod: mcpcmd.ClientAuthNone, Authority: authority}
			var deviceID string
			if device {
				started, err := operations.BeginDevice(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				deviceID = started.ID
				close(oauth.release)
			} else {
				started, err := operations.BeginBrowser(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				location, err := url.Parse(started.AuthorizationURL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := operations.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: location.Query().Get("state"), Code: "one-use-code", Issuer: resource, Authority: authority}); !errors.Is(err, mcpcmd.ErrConflict) {
					t.Fatalf("stale binding completion = %v, want conflict", err)
				}
			}
			select {
			case err := <-bound:
				if !errors.Is(err, mcpcmd.ErrConflict) {
					t.Fatalf("binding after edit = %v, want conflict", err)
				}
			case <-time.After(time.Second):
				t.Fatal("completion did not attempt binding")
			}
			item, err := operations.authorizationItem(t.Context(), created.Connection.ID)
			if err != nil || item.Connection.CurrentRevisionID == created.Connection.CurrentRevisionID || item.Definition.URL != resource+"/edited" || item.Definition.AuthBinding != nil {
				t.Fatalf("completion changed edited definition: %+v/%v", item, err)
			}
			binding := mcpcmd.AuthBinding{ConnectionID: created.Connection.ID, Resource: resource, Issuer: resource, ClientID: "worker-client"}
			saved, found, err := provider.MCP().GetMCPGrant(t.Context(), binding)
			if err != nil || !found || saved.Status != mcpcmd.GrantAuthorized {
				t.Fatalf("isolated saved grant = %s/%v/%v", saved.Status, found, err)
			}
			if device {
				status, err := operations.Device(t.Context(), deviceID, authority)
				if err != nil || status.Status != mcpcmd.DeviceAuthorized {
					t.Fatalf("saved device authorization = %s/%v", status.Status, err)
				}
			}
		})
	}
}

func TestAuthorizationAdapterCapturesTrustedConfiguredRevisionAndRetriesSavedGrant(t *testing.T) {
	for _, device := range []bool{false, true} {
		name := "browser"
		if device {
			name = "device"
		}
		t.Run(name, func(t *testing.T) {
			var resource string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resource+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer upstream.Close()
			resource = upstream.URL
			operations, provider, _, credentials, authority := operationsFixture(t, map[string]agentconfig.MCPServerConfig{"file-worker": {Type: agentconfig.MCPServerTypeHTTP, URL: resource, Headers: map[string]string{"X-Worker": "private-file-value"}}})
			oauth := &authorizationOAuth{resource: resource, release: make(chan struct{})}
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(provider.MCP()), oauth)
			if err != nil {
				t.Fatal(err)
			}
			bindingDone := make(chan struct{})
			binder := authorizationBindingFunc(func(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
				defer close(bindingDone)
				return operations.definitions.BindAuthorization(ctx, request)
			})
			authorizations, err := mcpmanage.NewAuthorizations(grants, "https://backoffice.example.test/base/mcp/oauth/callback", binder)
			if err != nil {
				t.Fatal(err)
			}
			defer authorizations.Close()
			if err := operations.ConfigureAuthorizations(authorizations); err != nil {
				t.Fatal(err)
			}
			request := mcpcmd.BeginAuthorization{ConnectionID: "config:file-worker", Scopes: []string{"tools"}, ClientID: "worker-client", ClientAuthMethod: mcpcmd.ClientAuthNone, Authority: authority}
			var browser mcpcmd.BrowserAuthorization
			var attemptID string
			if device {
				started, err := operations.BeginDevice(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				attemptID = started.ID
			} else {
				browser, err = operations.BeginBrowser(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				attemptID = browser.ID
			}
			attempt, found, err := operations.CurrentAttempt(t.Context(), "config:file-worker", authority)
			if err != nil || !found || attempt.ID != attemptID || attempt.Device != device {
				t.Fatalf("current configured attempt = %+v/%v/%v", attempt, found, err)
			}
			revisions, err := provider.MCP().ListMCPRevisions(t.Context())
			if err != nil || len(revisions) != 1 || revisions[0].Definition.URL != resource {
				t.Fatal("begin did not capture trusted configuration")
			}
			original := revisions[0]
			values, err := credentials.ResolveValues(original)
			if err != nil || values.Headers["X-Worker"] != "private-file-value" {
				t.Fatal("capture lost protected file values")
			}
			if device {
				close(oauth.release)
				// DeviceAuthorized precedes asynchronous binding/publication.
				// Read the complete adapter outcome after its actual bind returns.
				select {
				case <-bindingDone:
				case <-time.After(10 * time.Second):
					t.Fatal("device completion did not finish binding")
				}
				status, err := operations.Device(t.Context(), attemptID, authority)
				if err != nil || status.Status != mcpcmd.DeviceAuthorized {
					t.Fatalf("saved device authorization = %s/%v", status.Status, err)
				}
			} else {
				location, err := url.Parse(browser.AuthorizationURL)
				if err != nil {
					t.Fatal(err)
				}
				item, err := operations.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: location.Query().Get("state"), Code: "one-use-code", Issuer: resource, Authority: authority})
				if err != nil || item.Authorization != mcpcmd.GrantAuthorized || item.Status == mcpcmd.StatusReady {
					t.Fatalf("saved authorization vs readiness = %s/%s/%v", item.Authorization, item.Status, err)
				}
			}
			item, err := operations.authorizationItem(t.Context(), original.ConnectionID)
			if err != nil || item.Authorization != mcpcmd.GrantAuthorized || item.Definition.AuthBinding == nil {
				t.Fatal("saved grant was not selected")
			}
			before := oauth.exchanges
			wrong := mcpcmd.AuthBinding{ConnectionID: "other", Resource: "https://foreign.example.test", Issuer: "https://foreign.example.test", ClientID: "foreign-client"}
			retry, _ := operations.RetryAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: item.Connection.ID, ExpectedRevisionID: item.Connection.CurrentRevisionID, Binding: wrong, Authority: authority})
			if retry.Connection.CurrentRevisionID != item.Connection.CurrentRevisionID || retry.Definition.AuthBinding == nil || *retry.Definition.AuthBinding != *item.Definition.AuthBinding || oauth.exchanges != before {
				t.Fatal("retry changed current binding/revision or repeated OAuth")
			}
			if _, err := operations.RetryAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: item.Connection.ID, ExpectedRevisionID: original.ID, Authority: authority}); !errors.Is(err, mcpcmd.ErrConflict) {
				t.Fatal("retry ignored exact current revision")
			}
			retained, found, err := provider.MCP().GetMCPRevision(t.Context(), original.ConnectionID, original.ID)
			if err != nil || !found || retained.Definition.AuthBinding != nil {
				t.Fatal("authorization rewrote historical capture")
			}
		})
	}
}
