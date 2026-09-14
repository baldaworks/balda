package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type testSkillCatalog struct {
	retained map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
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
		&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}},
		&testSkillReader{},
		SkillMetadataBudget{},
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindSnapshot(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := bound.Resolve(SkillSelector{
		Source: first.ID.Source,
		Name:   first.Name,
	})
	if err != nil {
		t.Fatalf("Resolve(qualified) error = %v", err)
	}
	if selection.Snapshot != snapshot.ID || selection.Ref.Source != first.ID.Source || selection.Ref.Revision != first.Revision {
		t.Fatalf("qualified selection = %+v, want exact first descriptor", selection)
	}
	_, err = bound.Resolve(SkillSelector{Name: "review"})
	if !errors.Is(err, ErrSkillAmbiguous) {
		t.Fatalf("Resolve(unqualified) error = %v, want ErrSkillAmbiguous", err)
	}
}

func TestSkillManagerPinsLeadingExplicitReferenceAndRemovesOnlyItsToken(t *testing.T) {
	t.Parallel()

	descriptor := skillDescriptor(runtimecatalogcmd.SourceKindPlugin, "team/one", "review", "revision-1", "skills/review/SKILL.md")
	snapshot := skillSnapshot("snapshot-1", descriptor)
	manager, err := NewSkillManager(&testSkillCatalog{
		retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot},
	}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	text, selection, err := manager.PinExplicit(
		context.Background(),
		string(snapshot.ID),
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
	snapshot := skillSnapshot("snapshot", first, second)
	manager, err := NewSkillManager(
		&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}},
		&testSkillReader{},
		SkillMetadataBudget{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.PinExplicit(context.Background(), string(snapshot.ID), "$skill:review inspect"); !errors.Is(err, ErrSkillAmbiguous) {
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
		retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
			snapshot.ID: snapshot,
		},
	}
	reader := &testSkillReader{}
	manager, err := NewSkillManager(catalog, reader, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindSnapshot(context.Background(), snapshot.ID)
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
		&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}},
		&testSkillReader{},
		SkillMetadataBudget{MaxItems: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := manager.SkillMetadataForSnapshot(context.Background(), snapshot.ID)
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
	manager, err := NewSkillManager(&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if manager.budget.MaxItems != defaultSkillMetadataMaxItems || manager.budget.MaxBytes != defaultSkillMetadataMaxBytes {
		t.Fatalf("default budget = %+v, want safe finite defaults", manager.budget)
	}
	byteLimited, err := NewSkillManager(
		&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{snapshot.ID: snapshot}},
		&testSkillReader{},
		SkillMetadataBudget{MaxItems: 10, MaxBytes: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := byteLimited.SkillMetadataForSnapshot(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Skills) != 0 || projection.Omitted != 1 {
		t.Fatalf("byte-limited projection = %+v, want whole descriptor omitted", projection)
	}
}

func TestSkillSelectionsRemainBoundToExplicitSessionSnapshots(t *testing.T) {
	t.Parallel()

	oldSkill := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "workspace", "review", "revision-old", "SKILL.md")
	newSkill := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "workspace", "review", "revision-new", "SKILL.md")
	oldSnapshot := skillSnapshot("snapshot-old", oldSkill)
	newSnapshot := skillSnapshot("snapshot-new", newSkill)
	catalog := &testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
		oldSnapshot.ID: oldSnapshot,
		newSnapshot.ID: newSnapshot,
	}}
	manager, err := NewSkillManager(catalog, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	firstSession, err := manager.BindSnapshot(context.Background(), oldSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondSession, err := manager.BindSnapshot(context.Background(), newSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstSelection, err := firstSession.Resolve(SkillSelector{Source: oldSkill.ID.Source, Name: oldSkill.Name})
	if err != nil {
		t.Fatal(err)
	}
	secondSelection, err := secondSession.Resolve(SkillSelector{Source: newSkill.ID.Source, Name: newSkill.Name})
	if err != nil {
		t.Fatal(err)
	}
	if firstSelection.Snapshot != oldSnapshot.ID || firstSelection.Ref.Revision != oldSkill.Revision {
		t.Fatalf("first selection = %+v, want old pinned session", firstSelection)
	}
	if secondSelection.Snapshot != newSnapshot.ID || secondSelection.Ref.Revision != newSkill.Revision {
		t.Fatalf("second selection = %+v, want new pinned session", secondSelection)
	}
}

func TestSkillManagerKeepsWorkspaceCatalogsIsolated(t *testing.T) {
	t.Parallel()

	alpha := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "alpha", "alpha-skill", "revision-a", "SKILL.md")
	beta := skillDescriptor(runtimecatalogcmd.SourceKindWorkspaceSkill, "beta", "beta-skill", "revision-b", "SKILL.md")
	alphaSnapshot := skillSnapshot("snapshot-a", alpha)
	betaSnapshot := skillSnapshot("snapshot-b", beta)
	manager, err := NewSkillManager(&testSkillCatalog{retained: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
		alphaSnapshot.ID: alphaSnapshot,
		betaSnapshot.ID:  betaSnapshot,
	}}, &testSkillReader{}, SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	alphaSession, err := manager.BindSnapshot(context.Background(), alphaSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alphaSession.Resolve(SkillSelector{Name: "beta-skill"}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("alpha Resolve(beta-skill) error = %v, want ErrSkillNotFound", err)
	}
	selection, err := alphaSession.Resolve(SkillSelector{Name: "alpha-skill"})
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
