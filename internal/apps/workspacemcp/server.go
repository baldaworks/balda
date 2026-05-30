package workspacemcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ToolError struct {
	Operation string `json:"operation" jsonschema:"tool name that produced the error"`
	Code      string `json:"code" jsonschema:"stable machine-readable error code"`
	Message   string `json:"message" jsonschema:"human-readable error message"`
}

type ToolOutcome struct {
	OK    bool       `json:"ok" jsonschema:"true when the tool completed successfully"`
	Error *ToolError `json:"error,omitempty" jsonschema:"error details when ok is false"`
}

func okOutcome() ToolOutcome {
	return ToolOutcome{OK: true}
}

func validationFailure(operation string, message string) (*mcp.CallToolResult, ToolOutcome) {
	return failure(operation, "validation_error", message)
}

func backendFailure(operation string, err error) (*mcp.CallToolResult, ToolOutcome) {
	return failure(operation, "backend_error", err.Error())
}

func failure(operation string, code string, message string) (*mcp.CallToolResult, ToolOutcome) {
	return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: message}},
		}, ToolOutcome{
			OK: false,
			Error: &ToolError{
				Operation: operation,
				Code:      code,
				Message:   message,
			},
		}
}

// WorkspaceService defines workspace sync operations.
type WorkspaceService interface {
	Import(ctx context.Context, sessionID string) error
	Export(ctx context.Context, sessionID string, commitMessage string) error
}

// RegisterTools adds workspace MCP tools to an existing server.
func RegisterTools(server *mcp.Server, svc WorkspaceService) {
	if server == nil || svc == nil {
		return
	}
	srv := &service{svc: svc}
	srv.registerTools(server)
}

type service struct {
	svc WorkspaceService
}

func (s *service) registerTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "balda.workspace.import",
		Description: "Rebase the session workspace branch onto the configured base branch. Requires workspace mode and discards uncommitted workspace changes before rebasing.",
	}, s.importTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "balda.workspace.export",
		Description: "Squash-merge the session workspace branch into the configured base branch and create a commit using the provided Conventional Commit message. Requires workspace mode.",
	}, s.exportTool)
}

type importInput struct {
	SessionID string `json:"session_id" jsonschema:"Balda session ID whose workspace should be rebased onto the configured base branch"`
}

type importOutput struct {
	ToolOutcome
}

func (s *service) importTool(ctx context.Context, _ *mcp.CallToolRequest, in importInput) (*mcp.CallToolResult, importOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		result, out := validationFailure("balda.workspace.import", "session_id is required")
		return result, importOutput{ToolOutcome: out}, nil
	}

	if err := s.svc.Import(ctx, in.SessionID); err != nil {
		result, out := backendFailure("balda.workspace.import", err)
		return result, importOutput{ToolOutcome: out}, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Workspace synced to base branch successfully"}},
	}, importOutput{ToolOutcome: okOutcome()}, nil
}

type exportInput struct {
	SessionID     string `json:"session_id" jsonschema:"Balda session ID whose workspace should be exported to the configured base branch"`
	CommitMessage string `json:"commit_message" jsonschema:"Conventional Commit message for the squash-merge commit"`
}

type exportOutput struct {
	ToolOutcome
}

func (s *service) exportTool(ctx context.Context, _ *mcp.CallToolRequest, in exportInput) (*mcp.CallToolResult, exportOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		result, out := validationFailure("balda.workspace.export", "session_id is required")
		return result, exportOutput{ToolOutcome: out}, nil
	}
	if strings.TrimSpace(in.CommitMessage) == "" {
		result, out := validationFailure("balda.workspace.export", "commit_message is required")
		return result, exportOutput{ToolOutcome: out}, nil
	}

	if err := s.svc.Export(ctx, in.SessionID, in.CommitMessage); err != nil {
		result, out := backendFailure("balda.workspace.export", err)
		return result, exportOutput{ToolOutcome: out}, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Workspace exported to base branch successfully"}},
	}, exportOutput{ToolOutcome: okOutcome()}, nil
}
