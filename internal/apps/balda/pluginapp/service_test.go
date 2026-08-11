package pluginapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/state"
)

const (
	testMarketplaceName = "demo-market"
	testPluginName      = "demo"
)

func TestServiceMarketplaceAndAvailableInstallFlow(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	marketplaceRoot := filepath.Join(t.TempDir(), "market")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", testPluginName))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "demo-market",
  "interface": { "displayName": "Demo Market" },
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" },
      "category": "Productivity"
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", testPluginName, "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.2.3",
  "description": "Demo plugin"
}`)

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   testMarketplaceName,
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	sources, err := svc.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces() error = %v", err)
	}
	if len(sources) != 1 || sources[0].Name != testMarketplaceName {
		t.Fatalf("sources = %#v, want %s", sources, testMarketplaceName)
	}

	upgrades, err := svc.UpgradeMarketplaces(context.Background(), "")
	if err != nil {
		t.Fatalf("UpgradeMarketplaces() error = %v", err)
	}
	if len(upgrades) != 1 || upgrades[0].Name != testMarketplaceName || upgrades[0].PluginCount != 1 {
		t.Fatalf("upgrades = %#v, want %s with 1 plugin", upgrades, testMarketplaceName)
	}

	available, err := svc.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("len(available) = %d, want 1", len(available))
	}
	if available[0].Name != testPluginName || available[0].Marketplace != testMarketplaceName || available[0].Category != "Productivity" {
		t.Fatalf("available[0] = %#v", available[0])
	}

	if err := svc.Install(context.Background(), testPluginName+"@"+testMarketplaceName); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "plugins", testPluginName, "plugin.json")); err != nil {
		t.Fatalf("installed plugin manifest missing: %v", err)
	}

	installed, err := svc.ListInstalled(context.Background())
	if err != nil {
		t.Fatalf("ListInstalled() error = %v", err)
	}
	if len(installed) != 1 || installed[0].Name != testPluginName {
		t.Fatalf("installed = %#v, want %s", installed, testPluginName)
	}

	available, err = svc.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable() after install error = %v", err)
	}
	if !available[0].Installed {
		t.Fatalf("available[0].Installed = false, want true")
	}

	if err := svc.RemoveInstalled(context.Background(), testPluginName); err != nil {
		t.Fatalf("RemoveInstalled() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "plugins", testPluginName)); !os.IsNotExist(err) {
		t.Fatalf("installed plugin dir still exists, stat err = %v", err)
	}
}

func TestServiceMarketplaceGitSourceFlow(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	repoRoot := filepath.Join(t.TempDir(), "market-repo")
	mustMkdirAll(t, filepath.Join(repoRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(repoRoot, "plugins", "demo"))
	mustWriteFile(t, filepath.Join(repoRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "git-market",
  "interface": { "displayName": "Git Market" },
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" },
      "category": "Utilities"
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(repoRoot, "plugins", "demo", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "2.0.0",
  "description": "Demo plugin from git"
}`)
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "add", ".")
	runGit(t, repoRoot, "commit", "-m", "initial marketplace")

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "git-market",
		Source: "file://" + repoRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	upgrades, err := svc.UpgradeMarketplaces(context.Background(), "git-market")
	if err != nil {
		t.Fatalf("UpgradeMarketplaces() error = %v", err)
	}
	if len(upgrades) != 1 || upgrades[0].PluginCount != 1 {
		t.Fatalf("upgrades = %#v, want one upgraded marketplace with one plugin", upgrades)
	}

	available, err := svc.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("len(available) = %d, want 1", len(available))
	}
	if available[0].Marketplace != "git-market" || available[0].Version != "2.0.0" {
		t.Fatalf("available[0] = %#v", available[0])
	}
	if _, err := os.Stat(filepath.Join(stateDir, "plugin-marketplaces", "git-market", "repo", ".agents", "plugins", "marketplace.json")); err != nil {
		t.Fatalf("cached marketplace repo missing: %v", err)
	}
	statuses, err := svc.ListMarketplaceStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaceStatuses() error = %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	if statuses[0].Kind != "git" || !statuses[0].Cached || !statuses[0].ManifestPresent || statuses[0].AvailablePlugins != 1 {
		t.Fatalf("statuses[0] = %#v", statuses[0])
	}
	if statuses[0].ResolvedRef == "" {
		t.Fatalf("statuses[0].ResolvedRef is empty")
	}
	status, ok, err := svc.GetMarketplaceStatus(context.Background(), "git-market")
	if err != nil {
		t.Fatalf("GetMarketplaceStatus() error = %v", err)
	}
	if !ok || status.Name != "git-market" {
		t.Fatalf("GetMarketplaceStatus() = (%#v, %t), want git-market", status, ok)
	}
}

func TestServiceMarketplaceReadsManifestPath(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	marketplaceRoot := filepath.Join(t.TempDir(), "market")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", "demo", ".plugin"))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "demo-market",
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" },
      "manifest_path": ".plugin/plugin.json",
      "category": "Developer Tools"
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", "demo", ".plugin", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.2.3",
  "description": "Demo plugin"
}`)

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "demo-market",
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	available, err := svc.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("len(available) = %d, want 1", len(available))
	}
	if available[0].Name != "demo" || available[0].Version != "1.2.3" {
		t.Fatalf("available[0] = %#v", available[0])
	}
}

func TestServiceMarketplaceSkipsPortableDotPluginManifestByDefault(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	marketplaceRoot := filepath.Join(t.TempDir(), "market")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", "demo", ".plugin"))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "demo-market",
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" },
      "category": "Developer Tools"
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", "demo", ".plugin", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "2.3.4",
  "description": "Demo plugin"
}`)

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "demo-market",
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	available, err := svc.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable() error = %v", err)
	}
	if len(available) != 0 {
		t.Fatalf("len(available) = %d, want 0", len(available))
	}
}

func TestServiceInstallReplacesExistingPluginAtomically(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	marketplaceRoot := filepath.Join(t.TempDir(), "market")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", "demo"))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "demo-market",
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" }
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", "demo", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "9.9.9",
  "description": "Fresh plugin"
}`)

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "demo-market",
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	existingRoot := filepath.Join(stateDir, "plugins", "demo")
	mustMkdirAll(t, existingRoot)
	mustWriteFile(t, filepath.Join(existingRoot, "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name":"demo",
  "version":"0.1.0",
  "description":"Old plugin"
}`)
	if err := svc.Install(context.Background(), "demo@demo-market"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(existingRoot, "plugin.json"))
	if err != nil {
		t.Fatalf("ReadFile(installed plugin.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"version": "9.9.9"`) && !strings.Contains(string(data), `"version":"9.9.9"`) {
		t.Fatalf("installed plugin manifest = %s, want upgraded version", string(data))
	}
	if _, err := os.Stat(existingRoot + ".bak-install"); !os.IsNotExist(err) {
		t.Fatalf("backup root still exists, stat err = %v", err)
	}
	if _, err := os.Stat(existingRoot + ".tmp-install"); !os.IsNotExist(err) {
		t.Fatalf("stage root still exists, stat err = %v", err)
	}
}

func TestServiceInstallSkipsWhenSameVersionAlreadyInstalled(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(context.Background(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()

	marketplaceRoot := filepath.Join(t.TempDir(), "market")
	mustMkdirAll(t, filepath.Join(marketplaceRoot, ".agents", "plugins"))
	mustMkdirAll(t, filepath.Join(marketplaceRoot, "plugins", "demo"))
	mustWriteFile(t, filepath.Join(marketplaceRoot, ".agents", "plugins", "marketplace.json"), `{
  "name": "demo-market",
  "plugins": [
    {
      "name": "demo",
      "source": { "source": "local", "path": "./plugins/demo" }
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", "demo", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.2.3",
  "description": "Marketplace plugin"
}`)

	svc, err := New(stateDir, provider.AppKV())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "demo-market",
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	existingRoot := filepath.Join(stateDir, "plugins", "demo")
	mustMkdirAll(t, existingRoot)
	mustWriteFile(t, filepath.Join(existingRoot, "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name":"demo",
  "version":"1.2.3",
  "description":"Installed plugin"
}`)
	markerPath := filepath.Join(existingRoot, "keep.txt")
	mustWriteFile(t, markerPath, "keep")

	if err := svc.Install(context.Background(), "demo@demo-market"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("marker file missing after skipped install: %v", err)
	}
	if _, err := os.Stat(existingRoot + ".bak-install"); !os.IsNotExist(err) {
		t.Fatalf("backup root exists after skipped install, stat err = %v", err)
	}
	if _, err := os.Stat(existingRoot + ".tmp-install"); !os.IsNotExist(err) {
		t.Fatalf("stage root exists after skipped install, stat err = %v", err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, string(out))
	}
}
