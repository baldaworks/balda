package pluginapp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

const (
	testMarketplaceName = "demo-market"
	testPluginName      = "demo"
)

func TestServiceMarketplaceAndAvailableInstallFlow(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	t.Cleanup(func() { _ = makeTreeWritable(stateDir) })
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
  "interface": { "displayName": "Demo Market" },
  "plugins": [
    {
      "name": "demo",
      "path": "./plugins/demo",
      "category": "Productivity"
    }
  ]
}`)
	mustWriteFile(t, filepath.Join(marketplaceRoot, "plugins", "demo", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "demo",
  "version": "1.2.3",
  "description": "Demo plugin"
}`)

	svc, err := NewManaged(stateDir, provider.AppKV(), provider.Plugins(), &recordingActivator{}, provider.PluginManagement())
	if err != nil {
		t.Fatalf("NewManaged() error = %v", err)
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
		t.Fatalf("sources = %#v, want demo-market", sources)
	}

	upgrades, err := svc.UpgradeMarketplaces(context.Background(), "")
	if err != nil {
		t.Fatalf("UpgradeMarketplaces() error = %v", err)
	}
	if len(upgrades) != 1 || upgrades[0].Name != testMarketplaceName || upgrades[0].PluginCount != 1 {
		t.Fatalf("upgrades = %#v, want demo-market with 1 plugin", upgrades)
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
	if len(available[0].Diagnostics) != 0 {
		t.Fatalf("canonical path diagnostics = %#v", available[0].Diagnostics)
	}

	if err := svc.Install(context.Background(), testPluginName+"@"+testMarketplaceName); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	install, found, err := provider.Plugins().GetPluginInstall(t.Context(), testPluginName)
	if err != nil || !found {
		t.Fatalf("managed installation found=%v, error=%v", found, err)
	}
	revision, found, err := provider.Plugins().GetPluginRevision(t.Context(), testPluginName, install.ActiveRevisionID)
	if err != nil || !found {
		t.Fatalf("managed revision found=%v, error=%v", found, err)
	}
	retainedRoot := filepath.Join(stateDir, filepath.FromSlash(revision.RelativeRoot))
	if _, err := os.Stat(filepath.Join(retainedRoot, "plugin.json")); err != nil {
		t.Fatal(err)
	}

	installed, err := svc.ListInstalled(context.Background())
	if err != nil {
		t.Fatalf("ListInstalled() error = %v", err)
	}
	if len(installed) != 1 || installed[0].Name != testPluginName {
		t.Fatalf("installed = %#v, want demo", installed)
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
	if _, found, err := provider.Plugins().GetPluginInstall(t.Context(), testPluginName); err != nil || found {
		t.Fatalf("removed selection found=%v, error=%v", found, err)
	}
	if _, err := os.Stat(retainedRoot); err != nil {
		t.Fatalf("retained revision after remove: %v", err)
	}
}

func TestServiceMarketplaceGitSourceFlow(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	t.Cleanup(func() { _ = makeTreeWritable(stateDir) })
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

	svc, err := NewManaged(stateDir, provider.AppKV(), provider.Plugins(), &recordingActivator{}, provider.PluginManagement())
	if err != nil {
		t.Fatalf("NewManaged() error = %v", err)
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
	if len(available[0].Diagnostics) != 1 || available[0].Diagnostics[0].Code != runtimecatalogcmd.DiagnosticMarketplaceSourcePathDeprecated {
		t.Fatalf("transitional path diagnostics = %#v", available[0].Diagnostics)
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
	t.Cleanup(func() { _ = makeTreeWritable(stateDir) })
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

	svc, err := NewManaged(stateDir, provider.AppKV(), provider.Plugins(), &recordingActivator{}, provider.PluginManagement())
	if err != nil {
		t.Fatalf("NewManaged() error = %v", err)
	}
	if err := svc.AddMarketplace(context.Background(), MarketplaceSource{
		Name:   "demo-market",
		Source: marketplaceRoot,
	}); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}

	if _, err := svc.UpgradeMarketplaces(t.Context(), "demo-market"); err != nil {
		t.Fatal(err)
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

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
