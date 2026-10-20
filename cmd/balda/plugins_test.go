package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/stateowner"
)

func pluginCLIFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return os.Chmod(path, 0o600)
		})
	})
	t.Chdir(dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	config := `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: fixture
balda:
  provider: balda_agent
  state_dir: state
`
	if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), config); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "market")
	files := map[string]string{
		".agents/plugins/marketplace.json":    `{"name":"manifest-market","plugins":[{"name":"demo","path":"plugins/demo"}]}`,
		"plugins/demo/plugin.json":            `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"1.0.0","description":"Demo plugin"}`,
		"plugins/demo/skills/review/SKILL.md": "---\nname: review\ndescription: Review changes.\n---\nReview instructions.\n",
		"plugins/demo/skills/plan/SKILL.md":   "---\nname: plan\ndescription: Plan changes.\n---\nPlan instructions.\n",
	}
	for path, body := range files {
		if err := writeFile(filepath.Join(root, path), body); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "state"), root
}

func executePluginCommand(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	cmd := pluginsCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return output.String(), err
}

func TestPluginCLIInstallPersistsManagedRevision(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	for _, args := range [][]string{{"marketplace", "add", root}, {"marketplace", "upgrade", "market"}, {"install", "demo@market"}} {
		if output, err := executePluginCommand(t, t.Context(), args...); err != nil {
			t.Fatalf("%v: output=%q error=%v", args, output, err)
		}
	}
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	install, found, err := provider.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found || !install.Enabled || install.ActiveRevisionID == "" || install.OriginMarketplace != "market" {
		t.Fatalf("managed install=%+v,found=%v,error=%v", install, found, err)
	}
	for _, args := range [][]string{{"list"}, {"show", "demo"}, {"list", "--available"}, {"marketplace", "list"}, {"marketplace", "show", "market"}} {
		if output, err := executePluginCommand(t, t.Context(), args...); err != nil || strings.Contains(output, root) {
			t.Fatalf("%v: output=%q error=%v", args, output, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, install.DataRelativePath)); err != nil {
		t.Fatal(err)
	}
}

func TestPluginCLIOwnerProcessFixture(t *testing.T) {
	dir := os.Getenv("BALDA_PLUGIN_OWNER_TEST_DIR")
	if dir == "" {
		t.Skip("process owner fixture")
	}
	owner, err := stateowner.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	fmt.Println("owner ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestPluginCLIRejectsActiveOwnerBeforeOpeningState(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, executable, "-test.run=^TestPluginCLIOwnerProcessFixture$")
	child.Env = append(os.Environ(), "BALDA_PLUGIN_OWNER_TEST_DIR="+dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := child.Wait(); err != nil {
			t.Errorf("owner helper: %v", err)
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "owner ready" {
		t.Fatal("owner process did not acquire lock")
	}
	for _, args := range [][]string{{"install", "demo@market"}, {"remove", "demo"}, {"marketplace", "add", root}, {"marketplace", "upgrade", "market"}, {"marketplace", "remove", "market"}} {
		output, err := executePluginCommand(t, t.Context(), args...)
		if err == nil || !strings.Contains(err.Error(), "runtime_active") || strings.Contains(output, "published") {
			t.Fatalf("%v: output=%q error=%v", args, output, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "balda.lock" {
		t.Fatalf("rejected commands mutated state: %v", entries)
	}
}

func TestPluginCLIFailureReleasesOwnershipAndCancellationStopsPreparation(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	if _, err := executePluginCommand(t, t.Context(), "marketplace", "add", root, "--ref", "invalid-local-ref"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("local ref error=%v", err)
	}
	if _, err := executePluginCommand(t, t.Context(), "remove", "missing"); !errors.Is(err, plugincmd.ErrNotFound) {
		t.Fatalf("missing removal error=%v", err)
	}
	owner, err := stateowner.Acquire(dir)
	if err != nil {
		t.Fatalf("failed command retained ownership: %v", err)
	}
	_ = owner.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := executePluginCommand(t, ctx, "install", "demo@market"); err == nil {
		t.Fatal("cancelled install succeeded")
	}
	if _, err := executePluginCommand(t, t.Context(), "marketplace", "add", root); err != nil {
		t.Fatalf("subsequent mutation error=%v", err)
	}
}

type pluginCompletionFailure struct{ pluginapp.PluginStore }

func (s pluginCompletionFailure) CompletePluginActivation(context.Context, string, time.Time) error {
	return errors.New("private completion diagnostic /protected/source")
}

func TestPluginCLIAcceptedPendingIsExplicit(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	catalog, err := catalogapp.NewMaintenanceRuntime(dir, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := pluginapp.NewManaged(dir, p.AppKV(), pluginCompletionFailure{PluginStore: p.Plugins()}, catalog, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddMarketplace(t.Context(), pluginapp.MarketplaceSource{Name: "market", Source: root}); err != nil {
		t.Fatal(err)
	}
	original := preparePluginCLI
	preparePluginCLI = func(context.Context, bool) (*pluginapp.Service, func(), error) { return service, func() {}, nil }
	defer func() { preparePluginCLI = original }()
	output, err := executePluginCommand(t, t.Context(), "install", "demo@market")
	if !errors.Is(err, plugincmd.ErrRecoveryPending) || !strings.Contains(err.Error(), "Accepted; recovery pending for demo revision ") || strings.Contains(err.Error(), "private") || strings.Contains(output, "Accepted and published") {
		t.Fatalf("pending output=%q error=%v", output, err)
	}
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || observed.Install == nil || len(observed.Pending) != 1 {
		t.Fatalf("accepted state=%+v error=%v", observed, err)
	}
	for _, args := range [][]string{{"list"}, {"show", "demo"}} {
		output, err := executePluginCommand(t, t.Context(), args...)
		if err != nil || !strings.Contains(output, "Accepted; recovery pending") {
			t.Fatalf("pending inspection %v output=%q error=%v", args, output, err)
		}
	}
}

type pluginCompletionCancellation struct {
	pluginapp.PluginStore
	cancel context.CancelFunc
}

func (s pluginCompletionCancellation) CompletePluginActivation(ctx context.Context, id string, at time.Time) error {
	if err := s.PluginStore.CompletePluginActivation(ctx, id, at); err != nil {
		return err
	}
	s.cancel()
	return nil
}

func TestPluginCLIPublishedOutcomeSurvivesLateCancellation(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	catalog, err := catalogapp.NewMaintenanceRuntime(dir, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service, err := pluginapp.NewManaged(dir, p.AppKV(), pluginCompletionCancellation{PluginStore: p.Plugins(), cancel: cancel}, catalog, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddMarketplace(t.Context(), pluginapp.MarketplaceSource{Name: "market", Source: root}); err != nil {
		t.Fatal(err)
	}
	original := preparePluginCLI
	preparePluginCLI = func(context.Context, bool) (*pluginapp.Service, func(), error) { return service, func() {}, nil }
	defer func() { preparePluginCLI = original }()
	output, err := executePluginCommand(t, ctx, "install", "demo@market")
	if err != nil || !strings.Contains(output, "Accepted and published: demo") {
		t.Fatalf("completed install output=%q error=%v", output, err)
	}
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || observed.Install == nil || len(observed.Pending) != 0 {
		t.Fatalf("completed state=%+v error=%v", observed, err)
	}
}
