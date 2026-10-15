package runtimecatalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestMaterializeSkillFromRoot(t *testing.T) {
	for _, relative := range []string{".", "selected"} {
		t.Run(relative, func(t *testing.T) {
			base := t.TempDir()
			selected := filepath.Join(base, relative)
			if err := os.MkdirAll(filepath.Join(selected, "references"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(selected, "SKILL.md"), []byte("---\nname: actual-name\ndescription: useful instructions\n---\nbody"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(selected, "references", "details.txt"), []byte("complete resource"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			loader, err := NewSourceLoader(SourceLimits{})
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "capture")
			t.Cleanup(func() { makeSourceTreeWritable(dest) })
			source, err := loader.MaterializeSkillFromRoot(root, relative, dest, runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-test"})
			if err != nil {
				t.Fatal(err)
			}
			if len(source.Skills) != 1 || source.Skills[0].Name != "actual-name" {
				t.Fatalf("skills=%+v", source.Skills)
			}
			data, err := os.ReadFile(filepath.Join(dest, "actual-name", "references", "details.txt"))
			if err != nil || string(data) != "complete resource" {
				t.Fatalf("resource=%q err=%v", data, err)
			}
		})
	}
}

func TestMaterializeSkillAnchoredContainment(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: escape\ndescription: unsafe\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Skip(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"escape", "../escape", outside} {
		if _, err := loader.MaterializeSkillFromRoot(root, relative, filepath.Join(t.TempDir(), "capture"), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-test"}); err == nil {
			t.Fatalf("accepted %q", relative)
		}
	}
}

func TestMaterializeSkillAnchoredRootReplacement(t *testing.T) {
	parent := t.TempDir()
	lexical := filepath.Join(parent, "source")
	if err := os.Mkdir(lexical, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("---\nname: stable\ndescription: Original authority\n---\nOriginal bytes")
	if err := os.WriteFile(filepath.Join(lexical, "SKILL.md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(lexical)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Rename(lexical, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: substituted\ndescription: Wrong authority\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lexical); err != nil {
		t.Skip(err)
	}
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "capture")
	t.Cleanup(func() { makeSourceTreeWritable(dest) })
	source, err := loader.MaterializeSkillFromRoot(root, ".", dest, runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-stable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Skills) != 1 || source.Skills[0].Name != "stable" {
		t.Fatalf("lexical replacement redirected capture: %+v", source.Skills)
	}
}

func TestMaterializeSkillRejectsUnsafeResourcesAndInvalidContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*testing.T, string)
		limits SourceLimits
	}{
		{name: "resource outside selected subtree", setup: func(t *testing.T, base string) {
			if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("outside selected"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../secret.txt", filepath.Join(base, "picked", "resource.txt")); err != nil {
				t.Skip(err)
			}
		}},
		{name: "invalid frontmatter", setup: func(t *testing.T, base string) {
			if err := os.WriteFile(filepath.Join(base, "picked", "SKILL.md"), []byte("---\nname: Invalid Name\ndescription: invalid\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized resource", limits: SourceLimits{MaxFileBytes: 128, MaxFiles: 10, MaxEntries: 20, MaxTotalBytes: 1024, MaxDiagnostics: 10}, setup: func(t *testing.T, base string) {
			if err := os.WriteFile(filepath.Join(base, "picked", "resource.txt"), make([]byte, 129), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "picked"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, "picked", "SKILL.md"), []byte("---\nname: picked\ndescription: Skill\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			test.setup(t, base)
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			loader, err := NewSourceLoader(test.limits)
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "capture")
			t.Cleanup(func() { makeSourceTreeWritable(dest) })
			if _, err := loader.MaterializeSkillFromRoot(root, "picked", dest, runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-unsafe"}); err == nil {
				t.Fatal("unsafe skill accepted")
			}
		})
	}
}
