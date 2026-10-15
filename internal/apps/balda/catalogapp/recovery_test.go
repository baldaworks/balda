package catalogapp

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestCurrentRecoveryForExactBoundWorkerGrant(t *testing.T) {
	for _, status := range []mcpcmd.GrantStatus{mcpcmd.GrantAuthRequired, mcpcmd.GrantDisconnected} {
		t.Run(string(status), func(t *testing.T) {
			p, original, _, mutation, credentials := hybridCatalogFixture(t)
			if err := p.MCP().SaveMCPConnection(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			mutation.ExpectedVersion = 1
			mutation.Connection.CurrentRevisionID = "revision-bound"
			mutation.Audit.ID = "bind-grant"
			resource := "https://worker.example/mcp"
			worker := newWorkerGrantFixture(t, p, credentials, mutation.Authority, mutation.Connection.ID, resource)
			worker.authorize(t)
			grant, found, err := p.MCP().GetMCPGrant(t.Context(), worker.binding)
			if err != nil || !found {
				t.Fatal("grant missing")
			}
			expected := grant.Generation
			grant.Generation++
			grant.Status = status
			grant.UpdatedAt = time.Now().UTC()
			operation := mcpcmd.GrantRenew
			var authority *mcpcmd.Authority
			if status == mcpcmd.GrantDisconnected {
				operation = mcpcmd.GrantDisconnect
				a := mutation.Authority
				a.At = grant.UpdatedAt
				authority = &a
			}
			worker.save(t, grant, mcpmanage.GrantSecrets{}, operation, expected, authority)
			revision, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: mutation.Connection.ID, ID: mutation.Connection.CurrentRevisionID, CreatedAt: mutation.Authority.At, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: resource, OAuth: true, AuthBinding: &worker.binding, Scopes: []string{"tools:read"}, Targets: mcpcmd.Targets{All: true}}}, mcpcmd.ValueEdits{})
			if err != nil {
				t.Fatal(err)
			}
			mutation.Revision = &revision
			bridge := mcpbridge.New(mcpfx.GrantCredentials{Grants: worker.grants}, nil)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bridge.Close(context.Background()) }()
			catalog, err := NewRuntime(original.stateDir, "", "", p, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.MCP().Shutdown(context.Background()) }()
			if err := catalog.PublishMCP(t.Context(), func() error { return p.MCP().SaveMCPConnection(t.Context(), mutation) }); err != nil {
				t.Fatal(err)
			}
			connection, found, err := p.MCP().GetMCPConnection(t.Context(), revision.ConnectionID)
			if err != nil || !found {
				t.Fatal("current connection missing")
			}
			recovery, err := catalog.CurrentMCPRecovery(t.Context(), mcpcmd.Item{Connection: connection})
			if err != nil || recovery != mcpcmd.RecoveryAuthorizationRequired {
				t.Fatalf("bound %s recovery = %s/%v", status, recovery, err)
			}
			stale := connection
			stale.CurrentRevisionID += "-missing"
			if recovery, _ := catalog.CurrentMCPRecovery(t.Context(), mcpcmd.Item{Connection: stale}); recovery != "" {
				t.Fatal("historical bound failure inherited current recovery")
			}
		})
	}
}
