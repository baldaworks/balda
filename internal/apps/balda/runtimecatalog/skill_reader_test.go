package runtimecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type staticSkillRootResolver struct {
	root string
	err  error
}

func (r staticSkillRootResolver) ResolveSkillRoot(context.Context, runtimecatalogcmd.SkillRef) (string, error) {
	return r.root, r.err
}

func TestSkillReaderLoadsPinnedContainedResourcesInDeterministicOrder(t *testing.T) {
	t.Parallel()

	root, ref := writeSkillReaderFixture(t)
	reader, err := NewSkillReader(staticSkillRootResolver{root: root}, SkillReadLimits{})
	if err != nil {
		t.Fatalf("NewSkillReader() error = %v", err)
	}
	loaded, err := reader.ReadSkill(context.Background(), runtimecatalogcmd.SkillReadRequest{
		Ref:          ref,
		MainResource: "skills/review/SKILL.md",
		Resources:    []string{"z.txt", "notes/a.txt"},
	})
	if err != nil {
		t.Fatalf("ReadSkill() error = %v", err)
	}
	if loaded.Instructions != "review instructions" {
		t.Fatalf("instructions = %q, want fixture body", loaded.Instructions)
	}
	if len(loaded.Resources) != 2 || loaded.Resources[0].Name != "notes/a.txt" || loaded.Resources[1].Name != "z.txt" {
		t.Fatalf("resources = %+v, want deterministic relative names", loaded.Resources)
	}
	if strings.Contains(loaded.Resources[0].Name, root) {
		t.Fatalf("resource name %q exposes host root", loaded.Resources[0].Name)
	}
}

func TestSkillReaderRejectsChangedRevisionWithoutRebinding(t *testing.T) {
	t.Parallel()

	root, ref := writeSkillReaderFixture(t)
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), []byte("newer instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSkillReader(staticSkillRootResolver{root: root}, SkillReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadSkill(context.Background(), runtimecatalogcmd.SkillReadRequest{
		Ref: ref, MainResource: "skills/review/SKILL.md",
	})
	if !errors.Is(err, ErrRevisionUnavailable) {
		t.Fatalf("ReadSkill() error = %v, want ErrRevisionUnavailable", err)
	}
}

func TestSkillReaderEnforcesResourceAndPromptBounds(t *testing.T) {
	t.Parallel()

	root, ref := writeSkillReaderFixture(t)
	tests := []struct {
		name    string
		limits  SkillReadLimits
		request runtimecatalogcmd.SkillReadRequest
	}{
		{
			name:   "traversal",
			limits: DefaultSkillReadLimits(),
			request: runtimecatalogcmd.SkillReadRequest{
				Ref: ref, MainResource: "skills/review/SKILL.md", Resources: []string{"../SKILL.md"},
			},
		},
		{
			name:   "absolute resource",
			limits: DefaultSkillReadLimits(),
			request: runtimecatalogcmd.SkillReadRequest{
				Ref: ref, MainResource: "skills/review/SKILL.md", Resources: []string{"/etc/passwd"},
			},
		},
		{
			name:   "file count",
			limits: SkillReadLimits{MaxFiles: 1, MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 20},
			request: runtimecatalogcmd.SkillReadRequest{
				Ref: ref, MainResource: "skills/review/SKILL.md", Resources: []string{"z.txt"},
			},
		},
		{
			name:   "main exceeds total",
			limits: SkillReadLimits{MaxFiles: 2, MaxFileBytes: 1 << 20, MaxTotalBytes: 2},
			request: runtimecatalogcmd.SkillReadRequest{
				Ref: ref, MainResource: "skills/review/SKILL.md",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader, err := NewSkillReader(staticSkillRootResolver{root: root}, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadSkill(context.Background(), test.request); err == nil {
				t.Fatal("ReadSkill() error = nil, want bounded rejection")
			} else if strings.Contains(err.Error(), root) {
				t.Fatalf("ReadSkill() error %q exposes host root", err)
			}
		})
	}
}

func TestSkillReaderRejectsEscapingSymlink(t *testing.T) {
	t.Parallel()

	root, _ := writeSkillReaderFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("must not be read"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "review", "outside.txt")); err != nil {
		t.Fatal(err)
	}
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := loader.InspectRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := runtimecatalogcmd.SkillRef{
		Source:   runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"},
		Revision: revision,
		Name:     "review",
	}
	reader, err := NewSkillReader(staticSkillRootResolver{root: root}, SkillReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadSkill(context.Background(), runtimecatalogcmd.SkillReadRequest{
		Ref: ref, MainResource: "skills/review/SKILL.md", Resources: []string{"outside.txt"},
	})
	if err == nil {
		t.Fatal("ReadSkill() error = nil, want escaping symlink rejection")
	}
	if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), "must not be read") {
		t.Fatalf("ReadSkill() error %q leaks escaping target", err)
	}
}

func writeSkillReaderFixture(t *testing.T) (string, runtimecatalogcmd.SkillRef) {
	t.Helper()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "review")
	if err := os.MkdirAll(filepath.Join(skillDir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json":               `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`,
		"skills/review/SKILL.md":    "review instructions",
		"skills/review/notes/a.txt": "a",
		"skills/review/z.txt":       "z",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := loader.InspectRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, runtimecatalogcmd.SkillRef{
		Source:   runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"},
		Revision: revision,
		Name:     "review",
	}
}
