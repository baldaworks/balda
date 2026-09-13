package mcpruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type testPluginDataResolver struct{ root string }

func (r testPluginDataResolver) PluginDataRoot(context.Context, runtimecatalogcmd.SourceID) (string, error) {
	return r.root, nil
}

type testSecretResolver struct {
	values map[string]string
	err    error
}

func (r testSecretResolver) ResolveSecret(_ context.Context, handle string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.values[handle], nil
}

func TestPluginResolverResolvesContainedArgvFilteredEnvAndLaunchSecrets(t *testing.T) {
	t.Parallel()

	mcpJSON := `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers":{"tools":{"type":"stdio","command":"./bin/server","args":["--root=${PLUGIN_ROOT}/skills","--data=${PLUGIN_DATA}/cache"],"env":{"TOKEN":"${BALDA_SECRET:token}","DROP":"private"},"cwd":"${PLUGIN_ROOT}"}}
}`
	archive, descriptor, dataRoot := retainedPluginMCP(t, mcpJSON, true)
	resolver, err := NewPluginResolver(
		archive,
		testPluginDataResolver{root: dataRoot},
		testSecretResolver{values: map[string]string{"token": "resolved-secret"}},
		PluginPolicy{AllowedEnvKeys: map[string]struct{}{"TOKEN": {}}, MaxConfigBytes: 1 << 20, MaxArgs: 8, MaxEnv: 8, MaxHeaders: 8, AllowedTransports: map[string]struct{}{"stdio": {}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolver.ResolveLaunch(context.Background(), descriptor)
	if err != nil {
		t.Fatalf("ResolveLaunch() error = %v", err)
	}
	if !filepath.IsAbs(config.Command) || filepath.Base(config.Command) != "server" || config.WorkingDir == "" {
		t.Fatalf("resolved process config = %+v, want contained executable and cwd", config)
	}
	if len(config.Args) != 2 || !strings.Contains(config.Args[0], "skills") || !strings.Contains(config.Args[1], filepath.Join(dataRoot, "cache")) {
		t.Fatalf("resolved args = %#v", config.Args)
	}
	if config.Env["TOKEN"] != "resolved-secret" || config.Env["PLUGIN_ROOT"] == "" || config.Env["PLUGIN_DATA"] != dataRoot {
		t.Fatalf("resolved env keys/values are incomplete: %#v", config.Env)
	}
	if _, ok := config.Env["DROP"]; ok {
		t.Fatalf("filtered env contains DROP: %#v", config.Env)
	}
}

func TestPluginResolverRejectsTraversalWithoutLeakingSecret(t *testing.T) {
	t.Parallel()

	mcpJSON := `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers":{"tools":{"type":"stdio","command":"./bin/server","args":["${PLUGIN_ROOT}/../escape"],"env":{"TOKEN":"${BALDA_SECRET:token}"}}}
}`
	archive, descriptor, dataRoot := retainedPluginMCP(t, mcpJSON, true)
	resolver, err := NewPluginResolver(
		archive,
		testPluginDataResolver{root: dataRoot},
		testSecretResolver{err: errors.New("resolved-secret-must-not-leak")},
		PluginPolicy{AllowedEnvKeys: map[string]struct{}{"TOKEN": {}}, MaxConfigBytes: 1 << 20, MaxArgs: 8, MaxEnv: 8, MaxHeaders: 8, AllowedTransports: map[string]struct{}{"stdio": {}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveLaunch(context.Background(), descriptor)
	if err == nil {
		t.Fatal("ResolveLaunch() error = nil, want traversal rejection")
	}
	if strings.Contains(err.Error(), "resolved-secret") || strings.Contains(err.Error(), dataRoot) {
		t.Fatalf("ResolveLaunch() error %q leaks secret or host path", err)
	}
}

func TestPluginResolverRejectsWritableDataSymlinkEscape(t *testing.T) {
	t.Parallel()

	mcpJSON := `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers":{"tools":{"type":"stdio","command":"./bin/server","cwd":"${PLUGIN_DATA}/cache"}}
}`
	archive, descriptor, dataRoot := retainedPluginMCP(t, mcpJSON, true)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dataRoot, "cache")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewPluginResolver(archive, testPluginDataResolver{root: dataRoot}, nil, PluginPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveLaunch(context.Background(), descriptor); err == nil || strings.Contains(err.Error(), outside) {
		t.Fatalf("ResolveLaunch() error = %q, want redacted symlink escape rejection", err)
	}
}

func retainedPluginMCP(t *testing.T, mcpJSON string, executable bool) (*runtimecatalog.RevisionArchive, runtimecatalogcmd.MCPServerDescriptor, string) {
	t.Helper()
	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "bin", "server"), []byte("binary"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "mcp.json"), []byte(mcpJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(pluginRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.MCPServers) != 1 {
		t.Fatalf("MCP descriptors = %+v, want 1", source.MCPServers)
	}
	archiveParent := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(archiveParent, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	archive, err := runtimecatalog.NewRevisionArchive(filepath.Join(archiveParent, "revisions"), runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Retain(context.Background(), pluginRoot, source.Descriptor); err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	return archive, source.MCPServers[0], dataRoot
}
