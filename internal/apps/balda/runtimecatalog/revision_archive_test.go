package runtimecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestRevisionArchiveKeepsOldSkillBytesAfterMutableSourceRefresh(t *testing.T) {
	t.Parallel()

	root, oldRef := writeSkillReaderFixture(t)
	archiveParent := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(archiveParent, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	archive, err := NewRevisionArchive(filepath.Join(archiveParent, "skill-revisions"), SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	oldDescriptor := runtimecatalogcmd.SourceDescriptor{ID: oldRef.Source, Revision: oldRef.Revision}
	if err := archive.Retain(context.Background(), root, oldDescriptor); err != nil {
		t.Fatalf("Retain(old) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), []byte("new instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	newRevision, err := loader.InspectRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Retain(context.Background(), root, runtimecatalogcmd.SourceDescriptor{
		ID: oldRef.Source, Revision: newRevision,
	}); err != nil {
		t.Fatalf("Retain(new) error = %v", err)
	}
	reader, err := NewSkillReader(archive, SkillReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	oldLoaded, err := reader.ReadSkill(context.Background(), runtimecatalogcmd.SkillReadRequest{
		Ref: oldRef, MainResource: "skills/review/SKILL.md",
	})
	if err != nil {
		t.Fatalf("ReadSkill(old) error = %v", err)
	}
	if oldLoaded.Instructions != "review instructions" {
		t.Fatalf("old instructions = %q, want retained bytes", oldLoaded.Instructions)
	}
	newRef := oldRef
	newRef.Revision = newRevision
	newLoaded, err := reader.ReadSkill(context.Background(), runtimecatalogcmd.SkillReadRequest{
		Ref: newRef, MainResource: "skills/review/SKILL.md",
	})
	if err != nil {
		t.Fatalf("ReadSkill(new) error = %v", err)
	}
	if newLoaded.Instructions != "new instructions" {
		t.Fatalf("new instructions = %q, want refreshed bytes", newLoaded.Instructions)
	}
}

func TestRevisionArchiveRepairsWritableCrashResidueBeforeReuse(t *testing.T) {
	t.Parallel()

	root, ref := writeSkillReaderFixture(t)
	archiveParent := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(archiveParent, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	archive, err := NewRevisionArchive(filepath.Join(archiveParent, "skill-revisions"), SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := runtimecatalogcmd.SourceDescriptor{ID: ref.Source, Revision: ref.Revision}
	if err := archive.Retain(context.Background(), root, descriptor); err != nil {
		t.Fatal(err)
	}
	retainedRoot, err := archive.ResolveSkillRoot(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		retainedRoot,
		filepath.Join(retainedRoot, "skills"),
	} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(retainedRoot, "skills", "review", "SKILL.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ResolveSkillRoot(context.Background(), ref); !errors.Is(err, ErrRevisionUnavailable) {
		t.Fatalf("ResolveSkillRoot(writable) error = %v, want ErrRevisionUnavailable", err)
	}
	if err := archive.Retain(context.Background(), root, descriptor); err != nil {
		t.Fatalf("Retain(repair) error = %v", err)
	}
	if _, err := archive.ResolveSkillRoot(context.Background(), ref); err != nil {
		t.Fatalf("ResolveSkillRoot(repaired) error = %v", err)
	}
	if !archivedTreeIsReadOnly(retainedRoot) {
		t.Fatal("retained tree remains writable after repair")
	}
}
