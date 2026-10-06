package mcpbackofficeapp

import (
	"errors"
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

func TestCreateAuthorizationReturnsSavedConnectionAndBoundNativeAttempt(t *testing.T) {
	for _, device := range []bool{false, true} {
		name := "browser onboarding"
		if device {
			name = "device onboarding"
		}
		t.Run(name, func(t *testing.T) {
			o, p, _, credentials, authority := operationsFixture(t, nil)
			const resource = "https://worker.example.test/mcp"
			oauth := &authorizationOAuth{resource: resource, release: make(chan struct{})}
			grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), oauth)
			if err != nil {
				t.Fatal(err)
			}
			flows, err := mcpmanage.NewAuthorizations(grants, "https://backoffice.example.test/mcp/oauth/callback", o.definitions)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(flows.Close)
			if err := o.ConfigureAuthorizations(flows); err != nil {
				t.Fatal(err)
			}
			create := mcpcmd.CreateDefinition{PublicID: "worker", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: resource, Targets: mcpcmd.Targets{All: true}}, Authority: authority}
			// Only the creation's trusted authority/ID may bind the new flow.
			begin := mcpcmd.BeginAuthorization{ConnectionID: "foreign", Scopes: []string{"tools"}, ClientID: "worker-client", ClientAuthMethod: mcpcmd.ClientAuthNone}
			var saved mcpcmd.Item
			var attemptConnection string
			if device {
				var started mcpcmd.DeviceAuthorization
				saved, started, err = o.CreateAndBeginDevice(t.Context(), create, begin)
				attemptConnection = started.ConnectionID
			} else {
				var started mcpcmd.BrowserAuthorization
				saved, started, err = o.CreateAndBeginBrowser(t.Context(), create, begin)
				attemptConnection = started.ConnectionID
				if err == nil {
					location, parseErr := url.Parse(started.AuthorizationURL)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					completed, completeErr := o.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: location.Query().Get("state"), Code: "one-use-code", Issuer: resource, Authority: authority})
					if completeErr != nil || completed.Authorization != mcpcmd.GrantAuthorized {
						t.Fatalf("new connection authorization could not complete: %v", completeErr)
					}
				}
			}
			if err != nil || saved.Connection.ID == "" || attemptConnection != saved.Connection.ID || !saved.Definition.OAuth || saved.Status == mcpcmd.StatusReady {
				t.Fatalf("onboarding lost saved identity or claimed ready: %v", err)
			}
			connections, err := p.MCP().ListMCPConnections(t.Context())
			if err != nil || len(connections) != 1 {
				t.Fatal("onboarding did not create exactly one connection")
			}
		})
	}
}

func TestCreateAuthorizationKeepsSavedIdentityWhenDiscoveryFails(t *testing.T) {
	o, p, _, credentials, authority := operationsFixture(t, nil)
	grants, err := mcpmanage.NewGrants(credentials, mcpfx.NewGrantStore(p.MCP()), &authorizationOAuth{resource: "https://different.example.test/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	flows, err := mcpmanage.NewAuthorizations(grants, "https://backoffice.example.test/mcp/oauth/callback", o.definitions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flows.Close)
	if err := o.ConfigureAuthorizations(flows); err != nil {
		t.Fatal(err)
	}
	create := mcpcmd.CreateDefinition{PublicID: "worker", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example.test/mcp", Targets: mcpcmd.Targets{All: true}}, Authority: authority}
	begin := mcpcmd.BeginAuthorization{ClientID: "worker-client", ClientAuthMethod: mcpcmd.ClientAuthNone}
	saved, started, err := o.CreateAndBeginBrowser(t.Context(), create, begin)
	if !errors.Is(err, mcpcmd.ErrInvalid) || saved.Connection.ID == "" || started.ID != "" {
		t.Fatalf("discovery error lost saved connection: %v", err)
	}
	current, found, err := p.MCP().GetMCPConnection(t.Context(), saved.Connection.ID)
	if err != nil || !found || current.PublicID != "worker" {
		t.Fatal("saved connection is unavailable for retry")
	}
	begin.ConnectionID, begin.Authority = current.ID, authority
	if _, err := o.BeginBrowser(t.Context(), begin); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatalf("saved retry changed failure: %v", err)
	}
	connections, err := p.MCP().ListMCPConnections(t.Context())
	if err != nil || len(connections) != 1 {
		t.Fatal("retry duplicated saved creation")
	}
	create.PublicID = "forbidden"
	create.Authority.UserVersion++
	failed, _, err := o.CreateAndBeginBrowser(t.Context(), create, begin)
	if !errors.Is(err, mcpcmd.ErrConflict) || failed.Connection.ID != "" {
		t.Fatalf("stale creation authority: error=%v, saved=%t", err, failed.Connection.ID != "")
	}
	connections, err = p.MCP().ListMCPConnections(t.Context())
	if err != nil || len(connections) != 1 {
		t.Fatal("stale authority mutated durable connections")
	}
}
