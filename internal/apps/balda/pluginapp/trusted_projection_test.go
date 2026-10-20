package pluginapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestTrustedProjectionReadsAcceptedDiscoveryWithoutSource(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	writeManagedPlugin(t, root, managementTestVersion, true)
	request := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(t.Context(), testPluginName); err != nil {
		t.Fatal(err)
	}
	pluginBefore, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	marketBefore, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewManaged(s.stateDir, p.AppKV(), p.Plugins(), s.managed.activator, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	s = fresh
	beforeAudit, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := s.InstalledInventory(t.Context())
	if err != nil || len(inventory) != 1 || inventory[0].Enabled || inventory[0].ExpectedState != "" || inventory[0].Revision != pluginBefore.Install.ActiveRevisionID {
		t.Fatalf("trusted inventory=%+v, error=%v", inventory, err)
	}
	detail, err := s.InstalledDetail(t.Context(), testPluginName)
	if err != nil || detail.ExpectedState != "" || len(detail.Skills) != 1 || detail.OriginAvailable {
		t.Fatalf("retained detail=%+v, error=%v", detail, err)
	}
	packages, err := s.ListAvailable(t.Context())
	if err != nil || len(packages) != 1 {
		t.Fatalf("accepted packages = %+v, error = %v", packages, err)
	}
	if packages[0].Marketplace != "market" || packages[0].SourceRoot != "" || packages[0].PluginPath != "" {
		t.Fatalf("package projection = %+v", packages[0])
	}
	statuses, err := s.ListMarketplaceStatuses(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].Source != "local" || statuses[0].AvailablePlugins != 1 {
		t.Fatalf("marketplace projection = %+v, error = %v", statuses, err)
	}
	for _, selector := range []string{"demo@market", "demo@index-identity", "demo"} {
		selected, found, err := s.GetAvailable(t.Context(), selector)
		if err != nil || !found || selected.Marketplace != "market" || selected.Revision == "" || !selected.Installable || selected.Stale {
			t.Fatalf("selector %s: selected=%+v, found=%v, error=%v", selector, selected, found, err)
		}
	}
	raw, err := json.Marshal([]any{packages, statuses, inventory, detail})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, s.stateDir, "Ship the release safely.", marketBefore.Token(), pluginBefore.Token()} {
		if strings.Contains(string(raw), private) {
			t.Errorf("read disclosed private field %q", private)
		}
	}
	afterPlugin, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	afterMarket, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	afterAudit, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	if afterPlugin.Token() != pluginBefore.Token() || afterMarket.Token() != marketBefore.Token() || len(afterAudit.Events) != len(beforeAudit.Events) {
		t.Fatal("read changed accepted selection/discovery/intent/audit")
	}
	if _, err := s.UpgradeMarketplaces(t.Context(), "market"); !errors.Is(err, plugincmd.ErrUnavailable) {
		t.Fatalf("refresh unavailable source error=%v", err)
	}
	stale, err := s.ListAvailable(t.Context())
	if err != nil || len(stale) != 1 || !stale[0].Stale || stale[0].Installable {
		t.Fatalf("stale discovery=%+v, error=%v", stale, err)
	}
	status, found, err := s.GetMarketplaceStatus(t.Context(), "market")
	if err != nil || !found || status.LastAttemptStatus != discoveryFailed || status.FailureCode == "" || status.LastRefreshedAt == "" {
		t.Fatalf("failed marketplace=%+v, found=%v,error=%v", status, found, err)
	}
}

func TestTrustedProjectionPendingRemovalRetainsInspection(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	writeManagedPlugin(t, root, managementTestVersion, true)
	request := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	s.managed.activator.(*recordingActivator).publishErr = errors.New("synthetic publication failure")
	if err := s.RemoveInstalled(t.Context(), testPluginName); !errors.Is(err, plugincmd.ErrRecoveryPending) {
		t.Fatalf("remove error=%v", err)
	}
	entries, err := s.ListInstalled(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("installed=%+v, error=%v", entries, err)
	}
	// A new unaccepted package remains inert even while retained removal is inspectable.
	inert := filepath.Join(s.stateDir, "plugins", "inert", "plugin.json")
	mustMkdirAll(t, filepath.Dir(inert))
	mustWriteFile(t, inert, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"inert"}`)
	before, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.InstalledDetail(t.Context(), testPluginName)
	if err != nil || !detail.Removed || !detail.RecoveryPending || detail.Enabled || detail.ExpectedState != "" || len(detail.Skills) != 1 {
		t.Fatalf("pending removal detail=%+v,error=%v", detail, err)
	}
	inventory, err := s.InstalledInventory(t.Context())
	if err != nil || len(inventory) != 0 {
		t.Fatalf("active inventory=%+v,error=%v", inventory, err)
	}
	if _, err := s.InstalledDetail(t.Context(), "inert"); !errors.Is(err, plugincmd.ErrNotFound) {
		t.Fatalf("unaccepted detail error=%v", err)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), testPluginName)
	if err != nil || before.Token() != after.Token() {
		t.Fatalf("read changed pending removal: %v", err)
	}
}

func TestTrustedProjectionDefinitionWithoutDiscoveryIsSafe(t *testing.T) {
	s, p, _, _ := newMarketplaceManagementTestService(t)
	source := MarketplaceSource{Name: "saved", Source: "https://user:private-credential@example.com/market.git?private-query=yes#private-fragment", Ref: "stable", Sparse: []string{"plugins"}}
	if err := p.AppKV().SetJSON(t.Context(), marketplacePrefix+source.Name, source); err != nil {
		t.Fatal(err)
	}
	before, err := p.PluginManagement().ObserveMarketplace(t.Context(), source.Name)
	if err != nil {
		t.Fatal(err)
	}
	status, found, err := s.GetMarketplaceStatus(t.Context(), source.Name)
	if err != nil || !found || status.Cached || status.LastAttemptStatus != "" || status.AvailablePlugins != 0 {
		t.Fatalf("not-refreshed marketplace=%+v,found=%v,error=%v", status, found, err)
	}
	available, err := s.ListAvailable(t.Context())
	if err != nil || len(available) != 0 {
		t.Fatalf("available=%+v,error=%v", available, err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-credential", "private-query", "private-fragment", before.Token()} {
		if strings.Contains(string(raw), private) {
			t.Errorf("projection disclosed %q", private)
		}
	}
	after, err := p.PluginManagement().ObserveMarketplace(t.Context(), source.Name)
	if err != nil || before.Token() != after.Token() {
		t.Fatalf("read changed definition/discovery: %v", err)
	}
}
