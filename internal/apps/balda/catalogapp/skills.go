package catalogapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// managedSkillStore is origin-free: compilation reads accepted archive identities
// directly, never through management policy or mutable installation origins.
type managedSkillStore interface {
	ReadSelections(ctx context.Context) (runtimecatalogcmd.ManagedSkillSelection, error)
	MarkPublished(ctx context.Context, evidence runtimecatalogcmd.ManagedSkillSelection) error
}

// CaptureCandidate retains exactly one bounded skill from an anchored source.
func (r *Runtime) CaptureCandidate(ctx context.Context, root *os.Root, relative string, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, error) {
	if err := ctx.Err(); err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	if !isManagedSkillSource(id) {
		return runtimecatalogcmd.Source{}, skillcmd.ErrInvalid
	}
	stage, err := os.MkdirTemp(r.stateDir, ".skill-capture-")
	if err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	defer func() {
		_ = filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
		_ = os.RemoveAll(stage)
	}()
	materialized := filepath.Join(stage, "content")
	source, err := r.loader.MaterializeSkillFromRoot(root, relative, materialized, id)
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return runtimecatalogcmd.Source{}, skillcmd.ErrUnavailable
		}
		return runtimecatalogcmd.Source{}, skillcmd.ErrInvalid
	}
	if err := r.archive.Retain(ctx, materialized, source.Descriptor); err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	return source, nil
}

// PrepareSkillCandidate compiles a proposed complete managed selection before
// its guarded durable acceptance. Callers hold the common catalog mutation gate.
func (r *Runtime) PrepareSkillCandidate(ctx context.Context, selection runtimecatalogcmd.ManagedSkillSelection) (runtimecatalogcmd.PreparedApplicationCandidate, error) {
	plugins, err := r.currentPluginSources(ctx)
	if err != nil {
		return runtimecatalogcmd.PreparedApplicationCandidate{}, err
	}
	return r.prepareApplicationCandidate(ctx, plugins, nil, selection)
}

func (r *Runtime) managedSkillSources(ctx context.Context, selection runtimecatalogcmd.ManagedSkillSelection) ([]runtimecatalogcmd.Source, error) {
	sources := make([]runtimecatalogcmd.Source, 0, len(selection.Sources))
	seen := make(map[runtimecatalogcmd.SourceID]bool, len(selection.Sources))
	for _, descriptor := range selection.Normalized().Sources {
		if !isManagedSkillSource(descriptor.ID) || seen[descriptor.ID] {
			return nil, skillcmd.ErrConflict
		}
		seen[descriptor.ID] = true
		root, err := r.archive.ResolveRevisionRoot(ctx, descriptor)
		if err != nil {
			return nil, err
		}
		source, err := r.loader.LoadSkillSource(root, descriptor.ID)
		if err != nil || source.Descriptor != descriptor || len(source.Skills) != 1 || len(source.Diagnostics) != 0 {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (r *Runtime) validateManagedSkills(ctx context.Context, candidate runtimecatalogcmd.PreparedApplicationCandidate) error {
	current, err := r.managedSkills.ReadSelections(ctx)
	if err != nil {
		return err
	}
	if !current.Equal(candidate.ManagedSkills) {
		return skillcmd.ErrConflict
	}
	represented := runtimecatalogcmd.ManagedSkillSelection{Generation: current.Generation}
	for _, descriptor := range candidate.Sources {
		if isManagedSkillSource(descriptor.ID) {
			represented.Sources = append(represented.Sources, descriptor)
		}
	}
	if !represented.Equal(candidate.ManagedSkills) {
		return skillcmd.ErrConflict
	}
	return nil
}

// RetrySkillPublication rebuilds current desired state without fetching managed
// origins or replaying historical candidates. Caller holds the common gate.
func (r *Runtime) RetrySkillPublication(ctx context.Context) error {
	selection, err := r.managedSkills.ReadSelections(ctx)
	if err != nil {
		return err
	}
	candidate, err := r.PrepareSkillCandidate(ctx, selection)
	if err != nil {
		return err
	}
	return r.PublishCandidate(ctx, candidate)
}

func isManagedSkillSource(id runtimecatalogcmd.SourceID) bool {
	return id.Kind == runtimecatalogcmd.SourceKindUserSkill && strings.HasPrefix(id.Name, "managed-")
}
