package runtimecatalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// ErrRevisionUnavailable indicates that pinned source bytes cannot be read exactly.
var ErrRevisionUnavailable = runtimecatalogcmd.ErrRevisionUnavailable

// SkillRootResolver maps a pinned source revision to its trusted filesystem root.
type SkillRootResolver interface {
	ResolveSkillRoot(ctx context.Context, ref runtimecatalogcmd.SkillRef) (string, error)
}

// SkillReadLimits bound content added to one provider turn.
type SkillReadLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultSkillReadLimits returns conservative lazy-read limits.
func DefaultSkillReadLimits() SkillReadLimits {
	return SkillReadLimits{MaxFiles: 16, MaxFileBytes: 1 << 20, MaxTotalBytes: 2 << 20}
}

// SkillReader performs bounded, revision-checked reads beneath trusted source roots.
type SkillReader struct {
	roots  SkillRootResolver
	loader *SourceLoader
	limits SkillReadLimits
}

// NewSkillReader creates a revision-pinned reader.
func NewSkillReader(roots SkillRootResolver, limits SkillReadLimits) (*SkillReader, error) {
	if roots == nil {
		return nil, errors.New("skill root resolver is required")
	}
	if limits == (SkillReadLimits{}) {
		limits = DefaultSkillReadLimits()
	}
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return nil, errors.New("skill read limits must be positive")
	}
	loader, err := NewSourceLoader(SourceLimits{})
	if err != nil {
		return nil, err
	}
	return &SkillReader{roots: roots, loader: loader, limits: limits}, nil
}

// ReadSkill loads one exact SKILL.md and explicitly requested contained resources.
func (r *SkillReader) ReadSkill(
	ctx context.Context,
	request runtimecatalogcmd.SkillReadRequest,
) (runtimecatalogcmd.LoadedSkill, error) {
	if err := validateSkillReadRequest(request, r.limits.MaxFiles); err != nil {
		return runtimecatalogcmd.LoadedSkill{}, err
	}
	root, err := r.roots.ResolveSkillRoot(ctx, request.Ref)
	if err != nil {
		return runtimecatalogcmd.LoadedSkill{}, ErrRevisionUnavailable
	}
	revision, tree, err := r.loader.captureTree(root)
	if err != nil || revision != request.Ref.Revision {
		return runtimecatalogcmd.LoadedSkill{}, ErrRevisionUnavailable
	}
	main, err := readBoundedSkillFile(tree, request.MainResource, r.limits.MaxFileBytes)
	if err != nil {
		return runtimecatalogcmd.LoadedSkill{}, fmt.Errorf("read selected skill instructions: %w", err)
	}
	total := int64(len(main))
	if total > r.limits.MaxTotalBytes {
		return runtimecatalogcmd.LoadedSkill{}, errors.New("selected skill total-size limit exceeded")
	}
	loaded := runtimecatalogcmd.LoadedSkill{Ref: request.Ref, Instructions: string(main)}
	skillDir := filepath.Dir(filepath.FromSlash(request.MainResource))
	resources := append([]string(nil), request.Resources...)
	sort.Strings(resources)
	for _, resource := range resources {
		path := filepath.Join(skillDir, filepath.FromSlash(resource))
		data, err := readBoundedSkillFile(tree, path, r.limits.MaxFileBytes)
		if err != nil {
			return runtimecatalogcmd.LoadedSkill{}, fmt.Errorf("read selected skill resource %q: %w", resource, err)
		}
		total += int64(len(data))
		if total > r.limits.MaxTotalBytes {
			return runtimecatalogcmd.LoadedSkill{}, errors.New("selected skill total-size limit exceeded")
		}
		loaded.Resources = append(loaded.Resources, runtimecatalogcmd.SkillResource{Name: resource, Content: string(data)})
	}
	return loaded, nil
}

func validateSkillReadRequest(request runtimecatalogcmd.SkillReadRequest, maxFiles int) error {
	if request.Ref.Source.Kind == "" || strings.TrimSpace(request.Ref.Source.Name) == "" ||
		strings.TrimSpace(request.Ref.Name) == "" || strings.TrimSpace(string(request.Ref.Revision)) == "" {
		return errors.New("pinned skill reference is required")
	}
	cleanMain := filepath.ToSlash(filepath.Clean(strings.TrimSpace(request.MainResource)))
	if !validRelativeRef(cleanMain) || cleanMain != request.MainResource || filepath.Base(cleanMain) != "SKILL.md" {
		return errors.New("valid SKILL.md resource is required")
	}
	if len(request.Resources)+1 > maxFiles {
		return errors.New("selected skill file-count limit exceeded")
	}
	seen := make(map[string]struct{}, len(request.Resources))
	for _, resource := range request.Resources {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(resource)))
		if !validRelativeRef(clean) || clean == "SKILL.md" || clean != resource {
			return errors.New("invalid selected skill resource")
		}
		if _, ok := seen[clean]; ok {
			return errors.New("duplicate selected skill resource")
		}
		seen[clean] = struct{}{}
	}
	return nil
}

func readBoundedSkillFile(tree capturedTree, path string, maxBytes int64) ([]byte, error) {
	data, ok := tree.files[filepath.ToSlash(path)]
	if !ok {
		return nil, errors.New("resource unavailable")
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("resource exceeds size limit")
	}
	return append([]byte(nil), data...), nil
}
