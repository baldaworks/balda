package mcpmanage

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type overlappingDiagnosticProbe struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *overlappingDiagnosticProbe) DiagnoseMCP(ctx context.Context, _ mcpcmd.Revision) mcpcmd.DiagnosticResult {
	if p.calls.Add(1) == 1 {
		close(p.started)
		select {
		case <-p.release:
			return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 1}
		case <-ctx.Done():
			return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}
		}
	}
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}
}

func (p *overlappingDiagnosticProbe) DiagnoseMCPWithValues(ctx context.Context, revision mcpcmd.Revision, _ mcpcmd.LaunchValues) mcpcmd.DiagnosticResult {
	return p.DiagnoseMCP(ctx, revision)
}

func TestDiagnoseCurrentRetainsNewerCheckWhenOlderCompletes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		managed        bool
		changeRevision bool
	}{
		{name: "same configured revision"},
		{name: "changed configured revision", changeRevision: true},
		{name: "same managed revision", managed: true},
		{name: "changed managed revision", managed: true, changeRevision: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s, store, configured := definitionHarness(t)
			connectionID := "config:worker-tools"
			var managed mcpcmd.Item
			if tc.managed {
				var err error
				managed, err = s.Create(ctx, definitionCreate())
				if err != nil {
					t.Fatal(err)
				}
				connectionID = managed.Connection.ID
			} else {
				configured.items = []mcpcmd.Item{{
					Connection: mcpcmd.Connection{ID: connectionID, PublicID: "worker-tools", Source: mcpcmd.SourceConfig, Enabled: true},
					Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example.com/mcp", ConfigRevision: "revision-one"},
				}}
			}
			probe := &overlappingDiagnosticProbe{started: make(chan struct{}), release: make(chan struct{})}
			s.diagnostic = probe
			done := make(chan error, 1)
			go func() {
				_, err := s.DiagnoseCurrent(ctx, connectionID, definitionCreate().Authority)
				done <- err
			}()
			select {
			case <-probe.started:
			case <-ctx.Done():
				t.Fatal("first diagnostic did not start")
			}

			if tc.changeRevision {
				if tc.managed {
					next := definitionCreate()
					next.Definition.Command = "next-worker-mcp"
					if _, err := s.Update(ctx, mcpcmd.UpdateDefinition{ConnectionID: connectionID, ExpectedVersion: managed.Connection.Version, Definition: next.Definition, Enabled: true, Authority: next.Authority}); err != nil {
						t.Fatal(err)
					}
				} else {
					configured.items[0].Definition.ConfigRevision = "revision-next"
				}
			}
			writes := len(store.writes)
			current, err := s.DiagnoseCurrent(ctx, connectionID, definitionCreate().Authority)
			if err != nil {
				t.Fatal(err)
			}
			if current.Outcome != mcpcmd.DiagnosticFailed || current.ToolCount != 0 || current.CheckedAt.IsZero() {
				t.Fatalf("newer diagnostic = %+v, want a timestamped failure", current)
			}
			close(probe.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first diagnostic did not finish")
			}
			items, err := s.Inventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].LastDiagnostic == nil {
				t.Fatalf("current revision lost its diagnostic: %+v", items)
			}
			last := items[0].LastDiagnostic
			if last.Outcome != mcpcmd.DiagnosticFailed || last.ToolCount != 0 || !last.CheckedAt.Equal(current.CheckedAt) {
				t.Fatalf("last diagnostic = %+v, want the newer failed check", last)
			}
			if len(store.writes) != writes || probe.calls.Load() != 2 {
				t.Fatal("diagnostics or passive inventory changed definitions or reran a probe")
			}
		})
	}
}

type blockedDiagnosticConfiguration struct {
	*definitionConfigured
	started chan struct{}
	release chan struct{}
}

func (c *blockedDiagnosticConfiguration) MCPPrivateDefinition(ctx context.Context, id string) (mcpcmd.Definition, mcpcmd.LaunchValues, error) {
	definition, values, err := c.definitionConfigured.MCPPrivateDefinition(ctx, id)
	if err != nil || definition.ConfigRevision != "revision-one" {
		return definition, values, err
	}
	close(c.started)
	select {
	case <-c.release:
		return definition, values, nil
	case <-ctx.Done():
		return mcpcmd.Definition{}, mcpcmd.LaunchValues{}, ctx.Err()
	}
}

func TestDiagnoseCurrentRetainsNewerCheckAfterDelayedTargetLookup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, _, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{
		Connection: mcpcmd.Connection{ID: "config:worker-tools", PublicID: "worker-tools", Source: mcpcmd.SourceConfig, Enabled: true},
		Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example.com/mcp", ConfigRevision: "revision-one"},
	}}
	blocked := &blockedDiagnosticConfiguration{definitionConfigured: configured, started: make(chan struct{}), release: make(chan struct{})}
	s.configured = blocked
	probe := &recordingDiagnosticProbe{result: mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}}
	s.diagnostic = probe
	done := make(chan error, 1)
	go func() {
		_, err := s.DiagnoseCurrent(ctx, "config:worker-tools", definitionCreate().Authority)
		done <- err
	}()
	select {
	case <-blocked.started:
	case <-ctx.Done():
		t.Fatal("first target lookup did not start")
	}
	configured.items[0].Definition.ConfigRevision = "revision-next"
	current, err := s.DiagnoseCurrent(ctx, "config:worker-tools", definitionCreate().Authority)
	if err != nil {
		t.Fatal(err)
	}
	probe.result = mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 1}
	close(blocked.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("first diagnostic did not finish")
	}
	items, err := s.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LastDiagnostic == nil {
		t.Fatalf("current revision lost its diagnostic: %+v", items)
	}
	last := items[0].LastDiagnostic
	if last.Outcome != mcpcmd.DiagnosticFailed || last.ToolCount != 0 || !last.CheckedAt.Equal(current.CheckedAt) {
		t.Fatalf("last diagnostic = %+v, want the newer failed check", last)
	}
}
