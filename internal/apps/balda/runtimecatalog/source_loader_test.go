package runtimecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const testSkillName = "good"

func TestSourceLoaderLoadsPluginComponentsDeterministically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name":"release-tools",
  "ignored":true,
  "extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"release","description":"Release","instruction":"Deploy the release safely."}]}}
}`)
	writeSourceFile(t, root, "skills/deploy/SKILL.md", "---\nname: deploy\ndescription: Deploy releases safely.\n---\n# Deploy\n")
	writeSourceFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"remote":{"type":"streamable-http","url":"https://example.com/mcp"},"tools":{"type":"stdio","command":"go","args":["run","./cmd/tools"]}}}`)

	loader := newTestSourceLoader(t)
	first, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	second, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() second error = %v", err)
	}
	if first.Descriptor.Revision != second.Descriptor.Revision || len(first.Descriptor.Revision) != 64 {
		t.Fatalf("revision = %q, second = %q", first.Descriptor.Revision, second.Descriptor.Revision)
	}
	if len(first.Skills) != 1 || first.Skills[0].Name != "deploy" || first.Skills[0].Resource != "skills/deploy/SKILL.md" {
		t.Fatalf("skills = %#v", first.Skills)
	}
	if len(first.Commands) != 1 || first.Commands[0].Instruction != "Deploy the release safely." {
		t.Fatalf("commands = %#v", first.Commands)
	}
	if len(first.MCPServers) != 2 || first.MCPServers[0].Name != "remote" || first.MCPServers[1].Name != "tools" {
		t.Fatalf("MCP servers = %#v", first.MCPServers)
	}
	if !containsDiagnostic(first.Diagnostics, runtimecatalogcmd.DiagnosticManifestFieldIgnored) {
		t.Fatalf("diagnostics = %#v", first.Diagnostics)
	}

	writeSourceFile(t, root, "CHANGELOG.md", "changed")
	changed, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() changed error = %v", err)
	}
	if changed.Descriptor.Revision == first.Descriptor.Revision {
		t.Fatal("revision did not change with package content")
	}
}

func TestSourceLoaderValidatesBundledBaldaExtensionSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		extension   string
		wantCommand bool
	}{
		{
			name:        "valid inline instruction",
			extension:   `{"schema_version":1,"commands":[{"name":"release","description":"Release safely","instruction":"Inspect the release and deploy it."}]}`,
			wantCommand: true,
		},
		{
			name:      "closed command object",
			extension: `{"schema_version":1,"commands":[{"name":"release","description":"Release safely","instruction":"Deploy it.","unexpected":true}]}`,
		},
		{
			name:      "required instruction",
			extension: `{"schema_version":1,"commands":[{"name":"release","description":"Release safely"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","extensions":{"dev.baldaworks.balda":` + test.extension + `}}`
			writeSourceFile(t, root, "plugin.json", manifest)

			source, err := newTestSourceLoader(t).LoadPlugin(root)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCommand {
				if len(source.Commands) != 1 || source.Commands[0].Instruction != "Inspect the release and deploy it." {
					t.Fatalf("commands = %+v, want exact inline instruction", source.Commands)
				}
				if containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticExtensionInvalid) {
					t.Fatalf("diagnostics = %+v, want valid extension", source.Diagnostics)
				}
				return
			}
			if len(source.Commands) != 0 || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticExtensionInvalid) {
				t.Fatalf("source = %+v, want disabled extension diagnostic", source)
			}
		})
	}
}

func TestSourceLoaderRejectsFatalManifestAndTreeFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "manifest", setup: func(t *testing.T, root string) {
			writeSourceFile(t, root, "plugin.json", `{"$schema":"wrong","name":"demo"}`)
		}},
		{name: "file limit", setup: func(t *testing.T, root string) {
			writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
			writeSourceFile(t, root, "extra", "x")
		}},
		{name: "special file", setup: func(t *testing.T, root string) {
			writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
			makeSpecialFile(t, filepath.Join(root, "pipe"))
		}},
		{name: "escaping symlink", setup: func(t *testing.T, root string) {
			writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			limits := DefaultSourceLimits()
			if test.name == "file limit" {
				limits.MaxFiles = 1
			}
			loader, err := NewSourceLoader(limits)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.LoadPlugin(root); err == nil {
				t.Fatal("LoadPlugin() error = nil")
			}
		})
	}
}

func TestSourceLoaderMaterializesOnlySafeExactRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	destination := filepath.Join(t.TempDir(), "revision")
	t.Cleanup(func() { makeSourceTreeWritable(destination) })
	loader := newTestSourceLoader(t)

	materialized, err := loader.MaterializePlugin(root, destination)
	if err != nil {
		t.Fatalf("MaterializePlugin() error = %v", err)
	}
	reloaded, err := loader.LoadPlugin(destination)
	if err != nil {
		t.Fatalf("LoadPlugin(materialized) error = %v", err)
	}
	if reloaded.Descriptor.Revision != materialized.Source.Descriptor.Revision {
		t.Fatalf("revision = %q, want %q", reloaded.Descriptor.Revision, materialized.Source.Descriptor.Revision)
	}
	executable := filepath.Join(root, "server")
	writeSourceFile(t, root, "server", "#!/bin/sh\n")
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	executableDestination := filepath.Join(t.TempDir(), "executable-revision")
	t.Cleanup(func() { makeSourceTreeWritable(executableDestination) })
	if _, err := loader.MaterializePlugin(root, executableDestination); err != nil {
		t.Fatalf("MaterializePlugin(executable) error = %v", err)
	}
	info, err := os.Stat(filepath.Join(executableDestination, "server"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 || info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("materialized executable mode = %v", info.Mode())
	}

	unsafeRoot := t.TempDir()
	writeSourceFile(t, unsafeRoot, "plugin.json", validPluginManifest("unsafe"))
	if err := os.MkdirAll(filepath.Join(unsafeRoot, "skills", "unsafe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(unsafeRoot, "skills", "unsafe", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.MaterializePlugin(unsafeRoot, filepath.Join(t.TempDir(), "unsafe-revision")); err == nil {
		t.Fatal("MaterializePlugin() unsafe symlink error = nil")
	}
}

func makeSourceTreeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o200)
	})
}

func TestSourceLoaderIsolatesInvalidComponents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","extensions":{"dev.baldaworks.balda":{"schema_version":2,"commands":[]}}}`)
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	writeSourceFile(t, root, "skills/bad/SKILL.md", "no frontmatter")
	writeSourceFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"good":{"type":"stdio","command":"go"},"bad":{"type":"stdio","command":"go run x"}}}`)

	source, err := newTestSourceLoader(t).LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if len(source.Skills) != 1 || source.Skills[0].Name != testSkillName {
		t.Fatalf("skills = %#v", source.Skills)
	}
	if len(source.MCPServers) != 1 || source.MCPServers[0].Name != testSkillName {
		t.Fatalf("servers = %#v", source.MCPServers)
	}
	if len(source.Commands) != 0 {
		t.Fatalf("commands = %#v", source.Commands)
	}
	for _, code := range []string{runtimecatalogcmd.DiagnosticSkillInvalid, runtimecatalogcmd.DiagnosticMCPServerInvalid, runtimecatalogcmd.DiagnosticExtensionInvalid} {
		if !containsDiagnostic(source.Diagnostics, code) {
			t.Errorf("missing diagnostic %q in %#v", code, source.Diagnostics)
		}
	}
}

func TestSourceLoaderDisablesMalformedMCPWithoutDisablingSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	writeSourceFile(t, root, "mcp.json", `{"$schema":"wrong","mcpServers":{}}`)
	source, err := newTestSourceLoader(t).LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if len(source.Skills) != 1 || len(source.MCPServers) != 0 || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticMCPConfigInvalid) {
		t.Fatalf("source = %#v", source)
	}
}

func TestSourceLoaderIsolatesEscapingComponentPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "bad", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "mcp.json")); err != nil {
		t.Fatal(err)
	}

	source, err := newTestSourceLoader(t).LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if len(source.Skills) != 1 || source.Skills[0].Name != testSkillName {
		t.Fatalf("skills = %#v", source.Skills)
	}
	if !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticSkillInvalid) || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticMCPConfigInvalid) {
		t.Fatalf("diagnostics = %#v", source.Diagnostics)
	}
}

func TestSourceLoaderHashesSymlinkDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "a", "same")
	writeSourceFile(t, root, "b", "same")
	link := filepath.Join(root, "alias")
	if err := os.Symlink("a", link); err != nil {
		t.Fatal(err)
	}
	loader := newTestSourceLoader(t)
	first, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b", link); err != nil {
		t.Fatal(err)
	}
	second, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Descriptor.Revision == second.Descriptor.Revision {
		t.Fatal("revision did not change when symlink destination changed")
	}
}

func TestSourceLoaderBoundsDiagnosticsAndTotalBytes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	for _, name := range []string{"one", "two", "three"} {
		writeSourceFile(t, root, "skills/"+name+"/SKILL.md", "invalid")
	}
	limits := DefaultSourceLimits()
	limits.MaxDiagnostics = 2
	loader, err := NewSourceLoader(limits)
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %d, want 2", len(source.Diagnostics))
	}

	limits = DefaultSourceLimits()
	limits.MaxFileBytes = 1 << 20
	limits.MaxTotalBytes = int64(len(validPluginManifest("demo")) + 1)
	loader, err = NewSourceLoader(limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadPlugin(root); err == nil {
		t.Fatal("LoadPlugin() total-size error = nil")
	}
}

func TestSourceLoaderHashesAndBoundsDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"tools":{"type":"stdio","command":"go","cwd":"./data"}}}`)
	loader := newTestSourceLoader(t)
	withoutDirectory, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutDirectory.MCPServers) != 0 {
		t.Fatalf("servers without cwd = %#v", withoutDirectory.MCPServers)
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	withDirectory, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(withDirectory.MCPServers) != 1 || withDirectory.Descriptor.Revision == withoutDirectory.Descriptor.Revision {
		t.Fatalf("with directory = %#v, revision before = %q", withDirectory, withoutDirectory.Descriptor.Revision)
	}

	limits := DefaultSourceLimits()
	limits.MaxEntries = 2
	limited, err := NewSourceLoader(limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.LoadPlugin(root); err == nil {
		t.Fatal("LoadPlugin() entry-count error = nil")
	}
}

func TestSourceLoaderRejectsIncompleteBaldaCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"run"}]}}}`)
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	source, err := newTestSourceLoader(t).LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Commands) != 0 || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticExtensionInvalid) {
		t.Fatalf("source = %#v", source)
	}
}

func TestSourceLoaderLoadsConfiguredSkillSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "review/SKILL.md", "---\nname: review\ndescription: Review a change.\n---\n")
	loader := newTestSourceLoader(t)
	for _, kind := range []runtimecatalogcmd.SourceKind{runtimecatalogcmd.SourceKindUserSkill, runtimecatalogcmd.SourceKindWorkspaceSkill} {
		source, err := loader.LoadSkillSource(root, runtimecatalogcmd.SourceID{Kind: kind, Name: "configured"})
		if err != nil {
			t.Fatalf("LoadSkillSource(%q) error = %v", kind, err)
		}
		if len(source.Skills) != 1 || source.Skills[0].Resource != "review/SKILL.md" {
			t.Fatalf("skills = %#v", source.Skills)
		}
	}
}

func TestSourceLoaderIsolatesEscapingStandaloneSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "bad")); err != nil {
		t.Fatal(err)
	}
	source, err := newTestSourceLoader(t).LoadSkillSource(root, runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "configured"})
	if err != nil {
		t.Fatalf("LoadSkillSource() error = %v", err)
	}
	if len(source.Skills) != 1 || source.Skills[0].Name != testSkillName || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticSkillInvalid) {
		t.Fatalf("source = %#v", source)
	}
}

func TestSourceLoaderTreatsMCPDirectoryAsComponentFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: A valid skill.\n---\n")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "mcp.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "mcp.json", "escape")); err != nil {
		t.Fatal(err)
	}
	source, err := newTestSourceLoader(t).LoadPlugin(root)
	if err != nil {
		t.Fatalf("LoadPlugin() error = %v", err)
	}
	if len(source.Skills) != 1 || !containsDiagnostic(source.Diagnostics, runtimecatalogcmd.DiagnosticMCPConfigInvalid) {
		t.Fatalf("source = %#v", source)
	}
}

func TestSourceLoaderAppliesSizeLimits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSourceFile(t, root, "plugin.json", validPluginManifest("demo"))
	writeSourceFile(t, root, "large", strings.Repeat("x", 100))
	limits := DefaultSourceLimits()
	limits.MaxFileBytes = 64
	loader, err := NewSourceLoader(limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadPlugin(root); err == nil {
		t.Fatal("LoadPlugin() error = nil")
	}
}

func newTestSourceLoader(t *testing.T) *SourceLoader {
	t.Helper()
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return loader
}

func validPluginManifest(name string) string {
	return `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `"}`
}

func writeSourceFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsDiagnostic(diagnostics []runtimecatalogcmd.Diagnostic, code string) bool {
	for _, item := range diagnostics {
		if item.Code == code {
			return true
		}
	}
	return false
}
