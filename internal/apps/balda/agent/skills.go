package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const (
	defaultSkillMetadataMaxItems = 64
	defaultSkillMetadataMaxBytes = 16 << 10
)

var (
	// ErrSkillNotFound indicates that no eligible skill matches a selection.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrSkillAmbiguous indicates that an unqualified name has multiple matches.
	ErrSkillAmbiguous = errors.New("skill name is ambiguous; use a qualified source")
	// ErrSkillRevisionUnavailable indicates that a pinned snapshot or revision is unavailable.
	ErrSkillRevisionUnavailable = runtimecatalogcmd.ErrRevisionUnavailable
)

// TrustedSkillScope is host-owned scope for one provider turn. It is never a
// model-facing argument.
type TrustedSkillScope struct {
	Workspace string
}

// SkillCatalog is the agent layer's local immutable-snapshot port.
type SkillCatalog interface {
	CurrentSkillSnapshot(ctx context.Context, scope TrustedSkillScope) (runtimecatalogcmd.Snapshot, error)
	RetainedSkillSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error)
}

// SkillContentReader is the agent layer's local read-only content port.
type SkillContentReader interface {
	ReadSkill(ctx context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error)
}

// SkillMetadataBudget limits the metadata exposed in root instructions.
type SkillMetadataBudget struct {
	MaxItems int
	MaxBytes int
}

// SkillPromptMetadata contains selection metadata without a body or host path.
type SkillPromptMetadata struct {
	Source      runtimecatalogcmd.SourceID
	Name        string
	Description string
	Revision    runtimecatalogcmd.RevisionID
}

// SkillMetadataProjection is one deterministic, bounded prompt projection.
type SkillMetadataProjection struct {
	Snapshot runtimecatalogcmd.SnapshotID
	Skills   []SkillPromptMetadata
	Omitted  int
}

// SkillSelector is structured so delimiter-containing source names remain safe.
// A zero Source requests unique unqualified-name resolution.
type SkillSelector struct {
	Source runtimecatalogcmd.SourceID
	Name   string
}

// QualifiedSkillLoadRequest is the complete model-facing read-only operation.
// It deliberately has no workspace, snapshot, revision, root, or main-resource field.
type QualifiedSkillLoadRequest struct {
	SourceKind runtimecatalogcmd.SourceKind
	SourceName string
	SkillName  string
	Resources  []string
}

// SkillManager owns selection and turn-scoped loading policy.
type SkillManager struct {
	catalog SkillCatalog
	reader  SkillContentReader
	budget  SkillMetadataBudget
}

// NewSkillManager creates the agent-owned skill selection service.
func NewSkillManager(catalog SkillCatalog, reader SkillContentReader, budget SkillMetadataBudget) (*SkillManager, error) {
	if catalog == nil {
		return nil, errors.New("skill catalog is required")
	}
	if reader == nil {
		return nil, errors.New("skill reader is required")
	}
	if budget.MaxItems < 0 || budget.MaxBytes < 0 {
		return nil, errors.New("skill metadata limits must not be negative")
	}
	if budget.MaxItems == 0 {
		budget.MaxItems = defaultSkillMetadataMaxItems
	}
	if budget.MaxBytes == 0 {
		budget.MaxBytes = defaultSkillMetadataMaxBytes
	}
	return &SkillManager{catalog: catalog, reader: reader, budget: budget}, nil
}

// PinExplicit recognizes a leading $skill reference and resolves it before
// durable turn publication. Supported forms are $skill:<name> and the exact
// $skill:<source-kind>/<escaped-source-name>/<escaped-skill-name>.
func (m *SkillManager) PinExplicit(
	ctx context.Context,
	workspace,
	text string,
) (string, *runtimecatalogcmd.SkillSelection, error) {
	selector, remaining, found, err := parseExplicitSkillReference(text)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return text, nil, nil
	}
	selection, err := m.Resolve(ctx, TrustedSkillScope{Workspace: workspace}, selector)
	if err != nil {
		return "", nil, err
	}
	return remaining, &selection, nil
}

// SkillMetadata returns bounded metadata for trusted workspace scope.
func (m *SkillManager) SkillMetadata(ctx context.Context, workspace string) (SkillMetadataProjection, error) {
	bound, err := m.Bind(ctx, TrustedSkillScope{Workspace: workspace})
	if err != nil {
		return SkillMetadataProjection{}, err
	}
	return bound.Metadata(m.budget), nil
}

// Resolve pins a qualified or unique unqualified selection to the current snapshot.
func (m *SkillManager) Resolve(ctx context.Context, scope TrustedSkillScope, selector SkillSelector) (runtimecatalogcmd.SkillSelection, error) {
	bound, err := m.Bind(ctx, scope)
	if err != nil {
		return runtimecatalogcmd.SkillSelection{}, err
	}
	return bound.Resolve(selector)
}

// LoadPinned reads exactly the descriptor retained by a durable selection.
func (m *SkillManager) LoadPinned(
	ctx context.Context,
	selection runtimecatalogcmd.SkillSelection,
	resources []string,
) (runtimecatalogcmd.LoadedSkill, error) {
	if strings.TrimSpace(string(selection.Snapshot)) == "" {
		return runtimecatalogcmd.LoadedSkill{}, ErrSkillRevisionUnavailable
	}
	snapshot, err := m.catalog.RetainedSkillSnapshot(ctx, selection.Snapshot)
	if err != nil {
		return runtimecatalogcmd.LoadedSkill{}, ErrSkillRevisionUnavailable
	}
	if snapshot.ID != selection.Snapshot {
		return runtimecatalogcmd.LoadedSkill{}, ErrSkillRevisionUnavailable
	}
	descriptor, ok := findExactSkill(snapshot, selection.Ref.Source, selection.Ref.Name)
	if !ok || descriptor.Revision != selection.Ref.Revision {
		return runtimecatalogcmd.LoadedSkill{}, ErrSkillRevisionUnavailable
	}
	loaded, err := m.reader.ReadSkill(ctx, runtimecatalogcmd.SkillReadRequest{
		Ref:          selection.Ref,
		MainResource: descriptor.Resource,
		Resources:    append([]string(nil), resources...),
	})
	if err != nil {
		return runtimecatalogcmd.LoadedSkill{}, err
	}
	if loaded.Ref != selection.Ref {
		return runtimecatalogcmd.LoadedSkill{}, ErrSkillRevisionUnavailable
	}
	return loaded, nil
}

// BoundSkillLoader is a read-only provider adapter pinned to trusted turn state.
// Model-facing calls cannot replace its scope or snapshot.
type BoundSkillLoader struct {
	manager  *SkillManager
	snapshot runtimecatalogcmd.Snapshot
}

// ReadOnlySkillAdapter exposes a provider-native or bundled-MCP-shaped loader
// over one already-bound turn snapshot.
type ReadOnlySkillAdapter struct {
	bound *BoundSkillLoader
}

// NewReadOnlySkillAdapter creates a model-safe selection adapter.
func NewReadOnlySkillAdapter(bound *BoundSkillLoader) (*ReadOnlySkillAdapter, error) {
	if bound == nil || bound.manager == nil {
		return nil, errors.New("bound skill loader is required")
	}
	return &ReadOnlySkillAdapter{bound: bound}, nil
}

// Load resolves a structured qualified ID within trusted, pinned turn state.
func (a *ReadOnlySkillAdapter) Load(ctx context.Context, request QualifiedSkillLoadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	if a == nil || a.bound == nil {
		return runtimecatalogcmd.LoadedSkill{}, errors.New("read-only skill adapter is unavailable")
	}
	return a.bound.LoadQualified(ctx, runtimecatalogcmd.SourceID{
		Kind: request.SourceKind,
		Name: strings.TrimSpace(request.SourceName),
	}, strings.TrimSpace(request.SkillName), append([]string(nil), request.Resources...))
}

// Bind captures the current effective snapshot once for a provider turn.
func (m *SkillManager) Bind(ctx context.Context, scope TrustedSkillScope) (*BoundSkillLoader, error) {
	if m == nil || m.catalog == nil {
		return nil, errors.New("skill manager is unavailable")
	}
	snapshot, err := m.catalog.CurrentSkillSnapshot(ctx, TrustedSkillScope{Workspace: strings.TrimSpace(scope.Workspace)})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(snapshot.ID)) == "" {
		return nil, errors.New("current skill snapshot has no identity")
	}
	return &BoundSkillLoader{manager: m, snapshot: snapshot.Clone()}, nil
}

// Resolve selects within the already-pinned snapshot.
func (b *BoundSkillLoader) Resolve(selector SkillSelector) (runtimecatalogcmd.SkillSelection, error) {
	name := strings.TrimSpace(selector.Name)
	if name == "" {
		return runtimecatalogcmd.SkillSelection{}, fmt.Errorf("%w: empty name", ErrSkillNotFound)
	}
	qualified := selector.Source.Kind != "" || strings.TrimSpace(selector.Source.Name) != ""
	if qualified {
		if selector.Source.Kind == "" || strings.TrimSpace(selector.Source.Name) == "" {
			return runtimecatalogcmd.SkillSelection{}, fmt.Errorf("%w: incomplete source", ErrSkillNotFound)
		}
		descriptor, ok := findExactSkill(b.snapshot, selector.Source, name)
		if !ok {
			return runtimecatalogcmd.SkillSelection{}, fmt.Errorf("%w: qualified selection", ErrSkillNotFound)
		}
		return skillSelection(b.snapshot.ID, descriptor), nil
	}

	var matches []runtimecatalogcmd.SkillMetadata
	for _, descriptor := range b.snapshot.Skills {
		if descriptor.Name == name {
			matches = append(matches, descriptor)
		}
	}
	if len(matches) == 0 {
		return runtimecatalogcmd.SkillSelection{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	if len(matches) != 1 {
		return runtimecatalogcmd.SkillSelection{}, fmt.Errorf("%w: %s", ErrSkillAmbiguous, name)
	}
	return skillSelection(b.snapshot.ID, matches[0]), nil
}

// LoadQualified resolves a structured qualified ID and reads from this turn's snapshot.
func (b *BoundSkillLoader) LoadQualified(
	ctx context.Context,
	source runtimecatalogcmd.SourceID,
	name string,
	resources []string,
) (runtimecatalogcmd.LoadedSkill, error) {
	selection, err := b.Resolve(SkillSelector{Source: source, Name: name})
	if err != nil {
		return runtimecatalogcmd.LoadedSkill{}, err
	}
	return b.manager.LoadPinned(ctx, selection, resources)
}

// Metadata returns a deterministic whole-item projection of this turn's snapshot.
func (b *BoundSkillLoader) Metadata(budget SkillMetadataBudget) SkillMetadataProjection {
	ordered := make([]runtimecatalogcmd.SkillMetadata, 0, len(b.snapshot.Skills))
	for _, descriptor := range b.snapshot.Skills {
		ordered = append(ordered, descriptor)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].ID, ordered[j].ID
		if left.Source.Kind != right.Source.Kind {
			return left.Source.Kind < right.Source.Kind
		}
		if left.Source.Name != right.Source.Name {
			return left.Source.Name < right.Source.Name
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
	projection := SkillMetadataProjection{Snapshot: b.snapshot.ID}
	used := 0
	for _, descriptor := range ordered {
		metadata := SkillPromptMetadata{
			Source:      descriptor.ID.Source,
			Name:        descriptor.Name,
			Description: descriptor.Description,
			Revision:    descriptor.Revision,
		}
		encoded, _ := json.Marshal(metadata)
		if (budget.MaxItems > 0 && len(projection.Skills) >= budget.MaxItems) ||
			(budget.MaxBytes > 0 && used+len(encoded) > budget.MaxBytes) {
			projection.Omitted++
			continue
		}
		projection.Skills = append(projection.Skills, metadata)
		used += len(encoded)
	}
	return projection
}

func findExactSkill(snapshot runtimecatalogcmd.Snapshot, source runtimecatalogcmd.SourceID, name string) (runtimecatalogcmd.SkillMetadata, bool) {
	id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
	descriptor, ok := snapshot.Skills[id]
	return descriptor, ok
}

func skillSelection(snapshot runtimecatalogcmd.SnapshotID, descriptor runtimecatalogcmd.SkillMetadata) runtimecatalogcmd.SkillSelection {
	return runtimecatalogcmd.SkillSelection{
		Snapshot: snapshot,
		Ref: runtimecatalogcmd.SkillRef{
			Source:   descriptor.ID.Source,
			Revision: descriptor.Revision,
			Name:     descriptor.Name,
		},
	}
}

func parseExplicitSkillReference(text string) (SkillSelector, string, bool, error) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "$skill:") {
		return SkillSelector{}, text, false, nil
	}
	token, remaining := trimmed, ""
	if separator := strings.IndexFunc(trimmed, unicode.IsSpace); separator >= 0 {
		token, remaining = trimmed[:separator], trimmed[separator:]
	}
	raw := strings.TrimPrefix(token, "$skill:")
	parts := strings.Split(raw, "/")
	switch len(parts) {
	case 1:
		name, err := url.PathUnescape(parts[0])
		if err != nil || strings.TrimSpace(name) == "" {
			return SkillSelector{}, "", true, errors.New("invalid explicit skill reference")
		}
		return SkillSelector{Name: name}, strings.TrimLeft(remaining, " \t\r\n"), true, nil
	case 3:
		sourceName, sourceErr := url.PathUnescape(parts[1])
		name, nameErr := url.PathUnescape(parts[2])
		kind := runtimecatalogcmd.SourceKind(parts[0])
		if sourceErr != nil || nameErr != nil || kind == "" || strings.TrimSpace(sourceName) == "" || strings.TrimSpace(name) == "" {
			return SkillSelector{}, "", true, errors.New("invalid explicit skill reference")
		}
		return SkillSelector{
			Source: runtimecatalogcmd.SourceID{Kind: kind, Name: sourceName},
			Name:   name,
		}, strings.TrimLeft(remaining, " \t\r\n"), true, nil
	default:
		return SkillSelector{}, "", true, errors.New("invalid explicit skill reference")
	}
}
