package agentplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderLoad_ReturnsDiscoveredSkills(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	root := filepath.Join(stateDir, pluginsDirName, "demo")
	mustMkdirAll(t, filepath.Join(root, skillsDirName, "summarize"))
	mustWriteFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"`+pluginManifestSchemaV1+`","name":"demo","version":"1.0.0"}`)
	mustWriteFile(t, filepath.Join(root, skillsDirName, "summarize", skillFileName), "# Summarize\n\nUse this skill.")

	loader, err := NewLoader(stateDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	skills := catalog.Skills()
	if len(skills) != 1 {
		t.Fatalf("len(skills) = %d, want 1", len(skills))
	}
	if skills[0].PluginName != "demo" || skills[0].Name != "summarize" {
		t.Fatalf("skill = %+v, want demo/summarize", skills[0])
	}
}

func TestLoaderLoad_SkipsBrokenManifestPlugin(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	root := filepath.Join(stateDir, pluginsDirName, "broken")
	mustMkdirAll(t, root)
	mustWriteFile(t, filepath.Join(root, "plugin.json"), `{"name":""}`)

	loader, err := NewLoader(stateDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(catalog.Skills()); got != 0 {
		t.Fatalf("len(skills) = %d, want 0", got)
	}
}

func TestLoaderLoad_SkipsSkillWithoutSkillMarkdown(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	root := filepath.Join(stateDir, pluginsDirName, "demo")
	mustMkdirAll(t, filepath.Join(root, skillsDirName, "summarize"))
	mustWriteFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"`+pluginManifestSchemaV1+`","name":"demo"}`)

	loader, err := NewLoader(stateDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(catalog.Skills()); got != 0 {
		t.Fatalf("len(skills) = %d, want 0", got)
	}
}

func TestLoaderLoad_SkipsEscapingSkillSymlink(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	root := filepath.Join(stateDir, pluginsDirName, "demo")
	outside := filepath.Join(stateDir, "outside")
	mustMkdirAll(t, filepath.Join(root, skillsDirName, "summarize"))
	mustMkdirAll(t, outside)
	mustWriteFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"`+pluginManifestSchemaV1+`","name":"demo"}`)
	mustWriteFile(t, filepath.Join(outside, skillFileName), "# Outside\n")
	if err := os.Symlink(filepath.Join(outside, skillFileName), filepath.Join(root, skillsDirName, "summarize", skillFileName)); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	loader, err := NewLoader(stateDir)
	if err != nil {
		t.Fatalf("NewLoader() error = %v", err)
	}
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(catalog.Skills()); got != 0 {
		t.Fatalf("len(skills) = %d, want 0", got)
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
