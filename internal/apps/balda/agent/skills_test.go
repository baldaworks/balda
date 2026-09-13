package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type testSkillCatalog struct {
	current  runtimecatalogcmd.Snapshot
	retained map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
	scope    TrustedSkillScope
}

func (c *testSkillCatalog) CurrentSkillSnapshot(_ context.Context, scope TrustedSkillScope) (runtimecatalogcmd.Snapshot, error) {
	c.scope = scope
	return c.current.Clone(), nil
}

func (c *testSkillCatalog) RetainedSkillSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	snapshot, ok := c.retained[id]
	if !ok {
		return runtimecatalogcmd.Snapshot{}, errors.New("missing")
	}
	return snapshot.Clone(), nil
}

type testSkillReader struct {
	requests []runtimecatalogcmd.SkillReadRequest
}

type scopedSkillCatalog map[string]runtimecatalogcmd.Snapshot

func (c scopedSkillCatalog) CurrentSkillSnapshot(_ context.Context, scope TrustedSkillScope) (runtimecatalogcmd.Snapshot, error) {
	snapshot, ok := c[scope.Workspace]
	if !ok {
		return runtimecatalogcmd.Snapshot{}, errors.New("missing scope")
	}
	return snapshot.Clone(), nil
}

func (scopedSkillCatalog) RetainedSkillSnapshot(context.Context, runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	return runtimecatalogcmd.Snapshot{}, errors.New("unexpected retained snapshot read")
}

func (r *testSkillReader) ReadSkill(_ context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	r.requests = append(r.requests, request)
	return runtimecatalogcmd.LoadedSkill{Ref: request.Ref, Instructions: "pinned body"}, nil
}

func TestSkillManagerResolvesExactAndRejectsAmbiguousUnqualifiedNames(t *testing.T) {
	t.Parallel()

	first := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "team/one", "review", "rev-1", "skills/review/SKILL.md")
	second := skillDescriptor(runtimecatalogcmd.SourceKindUserSkill, "user", "review", "rev-2", "SKILL.md")
	snapshot := skillSnapshot("snapshot-1", first, second)
	manager, err := NewSkillManager(
		&testSkillCatalog{current: snapshot, retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}},
		&testSkillReader{},
		SkillMetadataBudget{},
	)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := manager.Resolve(context.Background(), TrustedSkillScope{Workspace: " /trusted/work "}, SkillSelector{
		Source: first.ID.Source,
		Name:   first.Name,
	})
	if err != nil {
		t.Fatalf("Resolve(qualified) error = %v", err)
	}
	if selection.Snapshot != snapshot.ID || selection.Ref.Source != first.ID.Source || selection.Ref.Revision != first.Revision {
		t.Fatalf("qualified selection = %+v, want exact first descriptor", selection)
	}
	_, err = manager.Resolve(context.Background(), TrustedSkillScope{}, SkillSelector{Name: "review"})
	if !errors.Is(err, ErrSkillAmbiguous) {
		t.Fatalf("Resolve(unqualified) error = %v, want ErrSkillAmbiguous", err)
	}
}

func TestSkillManagerPinsLeadingExplicitReferenceAndRemovesOnlyItsToken(t *testing.T) {
	t.Parallel()

	descriptor := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "team/one", "review", "revision-1", "skills/review/SKILL.md")
	snapshot := skillSnapshot("snapshot-1", descriptor)
	manager, err := NewSkillManager(&testSkillCatalog{current: snapshot}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	text, selection, err := manager.PinExplicit(
		context.Background(),
		"/trusted/workspace",
		"  $skill:plugin/team%2Fone/review\ninspect this",
	)
	if err != nil {
		t.Fatalf("PinExplicit() error = %v", err)
	}
	if text != "inspect this" {
		t.Fatalf("PinExplicit() text = %q, want request without reference", text)
	}
	if selection == nil || selection.Snapshot != snapshot.ID || selection.Ref != (runtimecatalogcmd.SkillRef{
		Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name,
	}) {
		t.Fatalf("PinExplicit() selection = %+v, want exact pinned descriptor", selection)
	}
}

func TestSkillManagerPinsUnqualifiedReferenceOnlyWhenUnique(t *testing.T) {
	t.Parallel()

	first := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "one", "review", "revision-1", "skills/review/SKILL.md")
	second := skillDescriptor(runtimecatalogcmd.SourceKindUserSkill, "two", "review", "revision-2", "SKILL.md")
	manager, err := NewSkillManager(
		&testSkillCatalog{current: skillSnapshot("snapshot", first, second)},
		&testSkillReader{},
		SkillMetadataBudget{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.PinExplicit(context.Background(), "workspace", "$skill:review inspect"); !errors.Is(err, ErrSkillAmbiguous) {
		t.Fatalf("PinExplicit() error = %v, want ErrSkillAmbiguous", err)
	}
}

func TestBoundSkillLoaderRetainsTurnRevisionAcrossRefresh(t *testing.T) {
	t.Parallel()

	oldSkill := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "demo", "review", "rev-old", "skills/review/SKILL.md")
	newSkill := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "demo", "review", "rev-new", "skills/review/SKILL.md")
	oldSnapshot := skillSnapshot("snapshot-old", oldSkill)
	newSnapshot := skillSnapshot("snapshot-new", newSkill)
	catalog := &testSkillCatalog{
		current: newSnapshot,
		retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
			oldSnapshot.ID: oldSnapshot,
			newSnapshot.ID: newSnapshot,
		},
	}
	reader := &testSkillReader{}
	manager, err := NewSkillManager(catalog, reader, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	selection := skillSelection(oldSnapshot.ID, oldSkill)
	loaded, err := manager.LoadPinned(context.Background(), selection, []string{"notes.md"})
	if err != nil {
		t.Fatalf("LoadPinned() error = %v", err)
	}
	if loaded.Ref.Revision != oldSkill.Revision || len(reader.requests) != 1 || reader.requests[0].Ref.Revision != oldSkill.Revision {
		t.Fatalf("loaded = %+v requests = %+v, want old revision", loaded, reader.requests)
	}
	if !reflect.DeepEqual(reader.requests[0].Resources, []string{"notes.md"}) {
		t.Fatalf("resources = %#v, want preserved relative request", reader.requests[0].Resources)
	}
}

func TestReadOnlySkillAdapterCannotChooseScopeSnapshotRevisionOrPath(t *testing.T) {
	t.Parallel()

	descriptor := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "demo", "review", "revision-1", "skills/review/SKILL.md")
	snapshot := skillSnapshot("snapshot-1", descriptor)
	catalog := &testSkillCatalog{
		current: snapshot,
		retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
			snapshot.ID: snapshot,
		},
	}
	reader := &testSkillReader{}
	manager, err := NewSkillManager(catalog, reader, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.Bind(context.Background(), TrustedSkillScope{Workspace: "/trusted/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewReadOnlySkillAdapter(bound)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Load(context.Background(), QualifiedSkillLoadRequest{
		SourceKind: runtimecatalogcmd.SourceKindPlugin,
		SourceName: "demo",
		SkillName:  "review",
		Resources:  []string{"guide.md"},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if catalog.scope.Workspace != "/trusted/workspace" {
		t.Fatalf("catalog scope = %+v, want host-bound workspace", catalog.scope)
	}
	if len(reader.requests) != 1 || reader.requests[0].MainResource != descriptor.Resource || reader.requests[0].Ref.Revision != descriptor.Revision {
		t.Fatalf("reader request = %+v, want host-resolved descriptor", reader.requests)
	}
}

func TestSkillMetadataProjectionIsDeterministicBoundedAndPathFree(t *testing.T) {
	t.Parallel()

	alpha := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "zeta", "alpha", "rev-z", "secret/root/SKILL.md")
	beta := skillDescriptor(runtimecatalogcmd.SourceKindBuiltin, "host", "beta", "rev-b", "other/SKILL.md")
	snapshot := skillSnapshot("snapshot", alpha, beta)
	manager, err := NewSkillManager(
		&testSkillCatalog{current: snapshot},
		&testSkillReader{},
		SkillMetadataBudget{MaxItems: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := manager.SkillMetadata(context.Background(), "/trusted/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Skills) != 1 || projection.Omitted != 1 || projection.Skills[0].Name != "beta" {
		t.Fatalf("projection = %+v, want sorted whole-item budget", projection)
	}
}

func TestSkillMetadataUsesSafeDefaultsAndByteBudget(t *testing.T) {
	t.Parallel()

	descriptor := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "demo", "review", "revision", "SKILL.md")
	snapshot := skillSnapshot("snapshot", descriptor)
	manager, err := NewSkillManager(&testSkillCatalog{current: snapshot}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if manager.budget.MaxItems != defaultSkillMetadataMaxItems || manager.budget.MaxBytes != defaultSkillMetadataMaxBytes {
		t.Fatalf("default budget = %+v, want safe finite defaults", manager.budget)
	}
	byteLimited, err := NewSkillManager(
		&testSkillCatalog{current: snapshot},
		&testSkillReader{},
		SkillMetadataBudget{MaxItems: 10, MaxBytes: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := byteLimited.SkillMetadata(context.Background(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Skills) != 0 || projection.Omitted != 1 {
		t.Fatalf("byte-limited projection = %+v, want whole descriptor omitted", projection)
	}
}

func TestSkillSelectionRefreshesOnlyBetweenBoundTurns(t *testing.T) {
	t.Parallel()

	oldSkill := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "workspace", "review", "revision-old", "SKILL.md")
	newSkill := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "workspace", "review", "revision-new", "SKILL.md")
	oldSnapshot := skillSnapshot("snapshot-old", oldSkill)
	newSnapshot := skillSnapshot("snapshot-new", newSkill)
	catalog := &testSkillCatalog{current: oldSnapshot}
	manager, err := NewSkillManager(catalog, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	firstTurn, err := manager.Bind(context.Background(), TrustedSkillScope{Workspace: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	catalog.current = newSnapshot
	secondTurn, err := manager.Bind(context.Background(), TrustedSkillScope{Workspace: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	firstSelection, err := firstTurn.Resolve(SkillSelector{Source: oldSkill.ID.Source, Name: oldSkill.Name})
	if err != nil {
		t.Fatal(err)
	}
	secondSelection, err := secondTurn.Resolve(SkillSelector{Source: newSkill.ID.Source, Name: newSkill.Name})
	if err != nil {
		t.Fatal(err)
	}
	if firstSelection.Snapshot != oldSnapshot.ID || firstSelection.Ref.Revision != oldSkill.Revision {
		t.Fatalf("first selection = %+v, want old pinned turn", firstSelection)
	}
	if secondSelection.Snapshot != newSnapshot.ID || secondSelection.Ref.Revision != newSkill.Revision {
		t.Fatalf("second selection = %+v, want refreshed new turn", secondSelection)
	}
}

func TestSkillManagerKeepsWorkspaceCatalogsIsolated(t *testing.T) {
	t.Parallel()

	alpha := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "alpha", "alpha-skill", "revision-a", "SKILL.md")
	beta := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "beta", "beta-skill", "revision-b", "SKILL.md")
	manager, err := NewSkillManager(scopedSkillCatalog{
		"/work/alpha": skillSnapshot("snapshot-a", alpha),
		"/work/beta":  skillSnapshot("snapshot-b", beta),
	}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	alphaTurn, err := manager.Bind(context.Background(), TrustedSkillScope{Workspace: "/work/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alphaTurn.Resolve(SkillSelector{Name: "beta-skill"}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("alpha Resolve(beta-skill) error = %v, want ErrSkillNotFound", err)
	}
	selection, err := alphaTurn.Resolve(SkillSelector{Name: "alpha-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Snapshot != "snapshot-a" || selection.Ref.Source.Name != "alpha" {
		t.Fatalf("alpha selection = %+v, want alpha-only snapshot", selection)
	}
}

func skillDescriptor(kind runtimecatalogcmd.SourceKind, sourceName, name string, revision runtimecatalogcmd.RevisionID, resource string) runtimecatalogcmd.SkillMetadata {
	source := runtimecatalogcmd.SourceID{Kind: kind, Name: sourceName}
	return runtimecatalogcmd.SkillMetadata{
		ID:          runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name},
		Revision:    revision,
		Name:        name,
		Description: name + " description",
		Resource:    resource,
	}
}

func skillSnapshot(id runtimecatalogcmd.SnapshotID, skills ...runtimecatalogcmd.SkillMetadata) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{ID: id, Skills: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata)}
	for _, skill := range skills {
		snapshot.Skills[skill.ID] = skill
	}
	return snapshot
}
