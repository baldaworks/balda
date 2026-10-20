package skillmcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testSnapshotResolver struct {
	id  runtimecatalogcmd.SnapshotID
	err error
}

func (r testSnapshotResolver) ResolveRuntimeSnapshot(context.Context, *mcp.CallToolRequest) (runtimecatalogcmd.SnapshotID, error) {
	return r.id, r.err
}

type testCatalog struct {
	snapshots map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
}

func (c testCatalog) RetainedSkillSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	snapshot, ok := c.snapshots[id]
	if !ok {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	return snapshot, nil
}

type testReader struct {
	requests []runtimecatalogcmd.SkillReadRequest
}

func (r *testReader) ReadSkill(_ context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	r.requests = append(r.requests, request)
	return runtimecatalogcmd.LoadedSkill{
		Ref:          request.Ref,
		Instructions: "review instructions",
		Resources:    []runtimecatalogcmd.SkillResource{{Name: "references/checklist.md", Content: "checklist"}},
	}, nil
}

func TestSkillLoaderUsesAuthenticatedSnapshotAndReturnsBoundedContent(t *testing.T) {
	t.Parallel()

	oldSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
	newSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "replacement"}
	oldDescriptor := skillDescriptor(oldSource, "review", "revision-old")
	newDescriptor := skillDescriptor(newSource, "review", "revision-new")
	reader := &testReader{}
	manager, err := baldaagent.NewSkillManager(testCatalog{snapshots: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
		"snapshot-old": skillSnapshot("snapshot-old", oldDescriptor),
		"snapshot-new": skillSnapshot("snapshot-new", newDescriptor),
	}}, reader, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cleanup, session := newTestSession(t, manager, testSnapshotResolver{id: "snapshot-old"})
	defer cleanup()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{
		"source_kind": "plugin",
		"source_name": "demo",
		"skill_name":  "review",
		"resources":   []any{"references/checklist.md"},
	}})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() result = %+v", result)
	}
	got := structuredResult(t, result)
	if got.Skill == nil || got.Skill.Instructions != "review instructions" || got.Skill.Ref.Revision != "revision-old" {
		t.Fatalf("loaded skill = %+v", got.Skill)
	}
	if len(reader.requests) != 1 || reader.requests[0].Ref.Source != oldSource || !slices.Equal(reader.requests[0].Resources, []string{"references/checklist.md"}) {
		t.Fatalf("reader requests = %+v", reader.requests)
	}

	failed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{
		"source_kind": "plugin", "source_name": "replacement", "skill_name": "review",
	}})
	if err != nil {
		t.Fatalf("CallTool(other snapshot) error = %v", err)
	}
	if !failed.IsError || len(reader.requests) != 1 {
		t.Fatalf("other-snapshot result = %+v, requests = %d", failed, len(reader.requests))
	}
	encoded, _ := json.Marshal(failed)
	for _, forbidden := range []string{"snapshot-old", "snapshot-new", "revision-old", "revision-new", "/workspace", "SKILL.md"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("failure leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSkillLoaderRejectsCallerControlledResourcePaths(t *testing.T) {
	t.Parallel()

	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
	reader := &testReader{}
	manager, err := baldaagent.NewSkillManager(testCatalog{snapshots: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
		"snapshot": skillSnapshot("snapshot", skillDescriptor(source, "review", "revision")),
	}}, rejectingReader{reader: reader}, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cleanup, session := newTestSession(t, manager, testSnapshotResolver{id: "snapshot"})
	defer cleanup()

	for _, resource := range []string{"../SKILL.md", "/etc/passwd"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{
			"source_kind": "plugin", "source_name": "demo", "skill_name": "review", "resources": []any{resource},
		}})
		if err != nil {
			t.Fatalf("CallTool(%q) error = %v", resource, err)
		}
		if !result.IsError {
			t.Fatalf("CallTool(%q) IsError = false", resource)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), resource) {
			t.Fatalf("failure leaked resource %q: %s", resource, encoded)
		}
	}
}

type rejectingReader struct{ reader *testReader }

func (r rejectingReader) ReadSkill(ctx context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	r.reader.requests = append(r.reader.requests, request)
	return runtimecatalogcmd.LoadedSkill{}, runtimecatalogcmd.ErrRevisionUnavailable
}

func newTestSession(t *testing.T, manager *baldaagent.SkillManager, resolver RuntimeSnapshotResolver) (context.Context, func(), *mcp.ClientSession) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "skill-loader-test", Version: "1"}, nil)
	RegisterTools(server, manager, resolver)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "skill-loader-client", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return ctx, func() { cancel(); _ = session.Close() }, session
}

func structuredResult(t *testing.T, result *mcp.CallToolResult) loadOutput {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out loadOutput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func skillDescriptor(source runtimecatalogcmd.SourceID, name string, revision runtimecatalogcmd.RevisionID) runtimecatalogcmd.SkillMetadata {
	id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
	return runtimecatalogcmd.SkillMetadata{ID: id, Name: name, Revision: revision, Resource: "skills/" + name + "/SKILL.md"}
}

func skillSnapshot(id runtimecatalogcmd.SnapshotID, skills ...runtimecatalogcmd.SkillMetadata) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{ID: id, Skills: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata)}
	for _, skill := range skills {
		snapshot.Skills[skill.ID] = skill
	}
	return snapshot
}
