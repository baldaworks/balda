package runtimecatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// RevisionArchive durably retains exact validated source bytes for turn retries.
type RevisionArchive struct {
	mu     sync.Mutex
	root   string
	loader *SourceLoader
}

// NewRevisionArchive creates a content archive under a trusted host directory.
func NewRevisionArchive(root string, limits SourceLimits) (*RevisionArchive, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("revision archive root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("resolve revision archive root")
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, errors.New("create revision archive root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, errors.New("resolve revision archive root")
	}
	loader, err := NewSourceLoader(limits)
	if err != nil {
		return nil, err
	}
	return &RevisionArchive{root: resolvedRoot, loader: loader}, nil
}

// Retain captures and materializes the descriptor's exact source revision.
func (a *RevisionArchive) Retain(ctx context.Context, sourceRoot string, descriptor runtimecatalogcmd.SourceDescriptor) error {
	if a == nil || a.loader == nil {
		return errors.New("revision archive is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if descriptor.ID.Kind == "" || strings.TrimSpace(descriptor.ID.Name) == "" || !validRevisionID(descriptor.Revision) {
		return errors.New("valid source descriptor is required")
	}
	revision, tree, err := a.loader.captureTree(sourceRoot)
	if err != nil {
		return fmt.Errorf("capture source revision: %w", err)
	}
	if revision != descriptor.Revision {
		return ErrRevisionUnavailable
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	sourceDir := filepath.Join(a.root, sourceArchiveKey(descriptor.ID))
	if err := ensureArchiveDirectory(sourceDir); err != nil {
		return err
	}
	destination := filepath.Join(sourceDir, string(descriptor.Revision))
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrRevisionUnavailable
		}
		retained, inspectErr := a.loader.InspectRevision(destination)
		if inspectErr != nil || retained != descriptor.Revision {
			return ErrRevisionUnavailable
		}
		return protectArchivedTree(destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect retained source revision")
	}
	stage, err := os.MkdirTemp(sourceDir, ".stage-")
	if err != nil {
		return errors.New("create source revision stage")
	}
	defer func() {
		_ = filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(stage)
	}()
	materialized := filepath.Join(stage, "content")
	if err := materializeCapturedTree(materialized, tree); err != nil {
		return fmt.Errorf("materialize source revision: %w", err)
	}
	if err := os.Chmod(materialized, 0o700); err != nil {
		return errors.New("prepare source revision publication")
	}
	if err := os.Rename(materialized, destination); err != nil {
		return errors.New("publish source revision")
	}
	return protectArchivedTree(destination)
}

// ResolveSkillRoot implements SkillRootResolver for retained source revisions.
func (a *RevisionArchive) ResolveSkillRoot(ctx context.Context, ref runtimecatalogcmd.SkillRef) (string, error) {
	return a.ResolveRevisionRoot(ctx, runtimecatalogcmd.SourceDescriptor{ID: ref.Source, Revision: ref.Revision})
}

// ResolveRevisionRoot returns one exact, read-only retained source root.
func (a *RevisionArchive) ResolveRevisionRoot(ctx context.Context, descriptor runtimecatalogcmd.SourceDescriptor) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a == nil || a.loader == nil || descriptor.ID.Kind == "" || strings.TrimSpace(descriptor.ID.Name) == "" || !validRevisionID(descriptor.Revision) {
		return "", ErrRevisionUnavailable
	}
	sourceDir := filepath.Join(a.root, sourceArchiveKey(descriptor.ID))
	for _, path := range []string{sourceDir, filepath.Join(sourceDir, string(descriptor.Revision))} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrRevisionUnavailable
		}
	}
	root := filepath.Join(sourceDir, string(descriptor.Revision))
	if !archivedTreeIsReadOnly(root) {
		return "", ErrRevisionUnavailable
	}
	revision, err := a.loader.InspectRevision(root)
	if err != nil || revision != descriptor.Revision {
		return "", ErrRevisionUnavailable
	}
	return root, nil
}

func protectArchivedTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("archived revision contains unsupported entry")
		}
		mode := os.FileMode(0o400)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o500
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return errors.New("protect source revision files")
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i], 0o500); err != nil {
			return errors.New("protect source revision directories")
		}
	}
	return nil
}

func archivedTreeIsReadOnly(root string) bool {
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o222 != 0 {
			return errors.New("writable archived revision")
		}
		return nil
	})
	return err == nil
}

func ensureArchiveDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return errors.New("create source revision directory")
		}
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("source revision directory is not managed")
	}
	return nil
}

func sourceArchiveKey(id runtimecatalogcmd.SourceID) string {
	data, _ := json.Marshal(id)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validRevisionID(revision runtimecatalogcmd.RevisionID) bool {
	value := string(revision)
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
