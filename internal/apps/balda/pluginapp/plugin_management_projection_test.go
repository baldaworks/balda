package pluginapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestManagementProjectionEmptyAndRetainedContributions(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	empty, err := s.ManagementInventory(t.Context(), a)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty inventory=%+v %v", empty, err)
	}
	writeManagedPlugin(t, root, managementTestVersion, true)
	pluginRoot := filepath.Join(root, "plugins", testPluginName)
	mustWriteFile(t, filepath.Join(pluginRoot, "skills", "ship", "SKILL.md"), "---\nname: ship\ndescription: Ship safely.\n---\nprivate-skill-body-canary\n")
	mustWriteFile(t, filepath.Join(pluginRoot, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"tools":{"type":"stdio","command":"go","env":{"SECRET":"private-environment-canary"}}}}`)
	request := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(t.Context(), testPluginName); err != nil {
		t.Fatal(err)
	}
	before, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	marketplace, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	// Inspect from retained immutable content after the external source is gone.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	inventory, err := s.ManagementInventory(t.Context(), a)
	if err != nil || len(inventory) != 1 {
		t.Fatalf("inventory=%+v %v", inventory, err)
	}
	detail, err := s.ManagementDetail(t.Context(), testPluginName, a)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != testPluginName || detail.Enabled || detail.OriginAvailable || detail.RecoveryPending || detail.ExpectedState == "" || len(detail.Commands) != 1 || len(detail.Skills) != 1 || len(detail.MCPServers) != 1 || len(detail.Revisions) != 1 {
		t.Fatalf("retained detail=%+v", detail)
	}
	if detail.Commands[0].Name != "ship" || detail.Skills[0].Name != "ship" || detail.MCPServers[0].Name != "tools" || detail.MCPServers[0].Transport != "stdio" || !detail.Revisions[0].Available {
		t.Fatalf("retained contributions=%+v", detail)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-skill-body-canary", "private-environment-canary", "Ship the release safely.", s.stateDir, root, "skills/ship/SKILL.md"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("detail disclosed private field %q", secret)
		}
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	afterMarketplace, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() || afterMarketplace.Token() != marketplace.Token() {
		t.Fatal("GET projection mutated accepted state")
	}
}

func TestManagementProjectionUsesSelectedRetainedDeclaration(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	request := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	first, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found {
		t.Fatalf("first install = %+v, found %t, error %v", first, found, err)
	}
	check := func(want bool) {
		t.Helper()
		inventory, err := s.ManagementInventory(t.Context(), a)
		if err != nil || len(inventory) != 1 {
			t.Fatalf("inventory = %+v, error %v", inventory, err)
		}
		detail, err := s.ManagementDetail(t.Context(), "demo", a)
		if err != nil {
			t.Fatal(err)
		}
		if inventory[0].DeclaresBaldaExtension != want || detail.DeclaresBaldaExtension != want {
			t.Fatalf("declaration: inventory = %t, detail = %t, want %t", inventory[0].DeclaresBaldaExtension, detail.DeclaresBaldaExtension, want)
		}
	}
	check(false)
	if err := s.Disable(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	writeManagedPlugin(t, root, "2.0.0", true)
	if err := s.Upgrade(t.Context(), "demo@market"); err != nil {
		t.Fatal(err)
	}
	check(true)
	second, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found {
		t.Fatalf("second install = %+v, found %t, error %v", second, found, err)
	}
	if err := s.Rollback(t.Context(), "demo", first.ActiveRevisionID); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := s.Rollback(t.Context(), "demo", second.ActiveRevisionID); err != nil {
		t.Fatal(err)
	}
	check(true)
	selected, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found {
		t.Fatalf("selected install = %+v, found %t, error %v", selected, found, err)
	}
	revision, found, err := p.Plugins().GetPluginRevision(t.Context(), "demo", selected.ActiveRevisionID)
	if err != nil || !found {
		t.Fatalf("selected revision = %+v, found %t, error %v", revision, found, err)
	}
	revisionRoot := filepath.Join(s.stateDir, filepath.FromSlash(revision.RelativeRoot))
	if err := os.Chmod(revisionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(revisionRoot, "drifted"), "changed")
	check(false)
	drifted, err := s.ManagementDetail(t.Context(), "demo", a)
	if err != nil || !drifted.Drifted || len(drifted.DiagnosticCodes) == 0 || drifted.DiagnosticCodes[0] != "revision_unavailable" {
		t.Fatalf("drifted detail = %+v, error %v", drifted, err)
	}
}

func TestManagementProjectionRequiresCurrentAuthority(t *testing.T) {
	s, p, a, _ := newMarketplaceManagementTestService(t)
	now := time.Now().UTC()
	event := usercmd.AuditEvent{ID: "revoke-projection", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
	if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementInventory(t.Context(), a); !errors.Is(err, plugincmd.ErrForbidden) {
		t.Fatalf("revoked inventory=%v", err)
	}
	if _, err := s.ManagementDetail(t.Context(), testPluginName, a); !errors.Is(err, plugincmd.ErrForbidden) {
		t.Fatalf("revoked detail=%v", err)
	}
}

func TestManagementProjectionReopensAcceptedPendingRemoval(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	writeManagedPlugin(t, root, managementTestVersion, true)
	request := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	expected, err := s.ManagementExpectedState(t.Context(), testPluginName, a)
	if err != nil {
		t.Fatal(err)
	}
	activator := &recordingActivator{publishErr: errors.New("synthetic remove publication failure")}
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Remove, PluginName: testPluginName, ExpectedState: expected, Confirm: true, Authority: a})
	if !errors.Is(err, plugincmd.ErrRecoveryPending) || !outcome.Accepted || !outcome.RecoveryPending {
		t.Fatalf("remove=%+v %v", outcome, err)
	}
	before, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.ManagementDetail(t.Context(), testPluginName, a)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Removed || detail.Enabled || !detail.RecoveryPending || detail.OriginAvailable || detail.ExpectedState == "" || detail.Name != testPluginName || len(detail.Commands) != 1 || len(detail.Revisions) != 1 || detail.Revisions[0].Current {
		t.Fatalf("pending removal detail=%+v", detail)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	if before.Token() != after.Token() || len(activator.published) != 0 {
		t.Fatal("reopening pending removal mutated or recovered accepted state")
	}
	activator.publishErr = nil
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementDetail(t.Context(), testPluginName, a); !errors.Is(err, plugincmd.ErrNotFound) {
		t.Fatalf("completed removed detail=%v", err)
	}
}
