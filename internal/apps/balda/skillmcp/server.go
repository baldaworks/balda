// Package skillmcp exposes snapshot-bound skill loading through Balda's
// authenticated bundled MCP server.
package skillmcp

import (
	"context"
	"strings"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolName = "balda.skill.load"

// RuntimeSnapshotResolver returns trusted runtime state attached outside the
// model-visible tool input.
type RuntimeSnapshotResolver interface {
	ResolveRuntimeSnapshot(ctx context.Context, req *mcp.CallToolRequest) (runtimecatalogcmd.SnapshotID, error)
}

type loadInput struct {
	SourceKind string   `json:"source_kind" jsonschema:"qualified catalog source kind exactly as advertised"`
	SourceName string   `json:"source_name" jsonschema:"qualified catalog source name exactly as advertised"`
	SkillName  string   `json:"skill_name" jsonschema:"skill name exactly as advertised"`
	Resources  []string `json:"resources,omitempty" jsonschema:"optional relative supporting resource paths from the selected skill"`
}

type toolError struct {
	Code    string `json:"code" jsonschema:"stable machine-readable error code"`
	Message string `json:"message" jsonschema:"bounded error message"`
}

type loadOutput struct {
	OK    bool                           `json:"ok" jsonschema:"true when the skill loaded successfully"`
	Skill *runtimecatalogcmd.LoadedSkill `json:"skill,omitempty" jsonschema:"snapshot-pinned skill instructions and requested resources"`
	Error *toolError                     `json:"error,omitempty" jsonschema:"bounded error details when ok is false"`
}

type service struct {
	skills   *baldaagent.SkillManager
	snapshot RuntimeSnapshotResolver
}

// RegisterTools adds the read-only skill loader to an existing MCP server.
func RegisterTools(server *mcp.Server, skills *baldaagent.SkillManager, snapshot RuntimeSnapshotResolver) {
	if server == nil || skills == nil || snapshot == nil {
		return
	}
	svc := &service{skills: skills, snapshot: snapshot}
	destructive, openWorld := false, false
	mcp.AddTool(server, &mcp.Tool{
		Name:        toolName,
		Description: "Load one advertised Balda skill from this session's immutable capability snapshot. Use the qualified source_kind, source_name, and skill_name from the available-skills metadata. The snapshot and revision are bound by Balda and cannot be selected by this call.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Load session skill",
			ReadOnlyHint:    true,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}, svc.load)
}

func (s *service) load(ctx context.Context, req *mcp.CallToolRequest, in loadInput) (*mcp.CallToolResult, loadOutput, error) {
	if strings.TrimSpace(in.SourceKind) == "" || strings.TrimSpace(in.SourceName) == "" || strings.TrimSpace(in.SkillName) == "" {
		return loadFailure("invalid_request", "qualified skill identity is required")
	}
	snapshot, err := s.snapshot.ResolveRuntimeSnapshot(ctx, req)
	if err != nil {
		return loadFailure("unavailable", "skill loader context is unavailable")
	}
	bound, err := s.skills.BindSnapshot(ctx, snapshot)
	if err != nil {
		return loadFailure("unavailable", "skill is unavailable")
	}
	adapter, err := baldaagent.NewReadOnlySkillAdapter(bound)
	if err != nil {
		return loadFailure("unavailable", "skill is unavailable")
	}
	loaded, err := adapter.Load(ctx, baldaagent.QualifiedSkillLoadRequest{
		SourceKind: runtimecatalogcmd.SourceKind(strings.TrimSpace(in.SourceKind)),
		SourceName: strings.TrimSpace(in.SourceName),
		SkillName:  strings.TrimSpace(in.SkillName),
		Resources:  append([]string(nil), in.Resources...),
	})
	if err != nil {
		return loadFailure("unavailable", "skill is unavailable")
	}
	return nil, loadOutput{OK: true, Skill: &loaded}, nil
}

func loadFailure(code, message string) (*mcp.CallToolResult, loadOutput, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}, loadOutput{Error: &toolError{Code: code, Message: message}}, nil
}
