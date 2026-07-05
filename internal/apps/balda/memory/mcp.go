package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	codeValidationError = "validation_error"
	codeBackendError    = "backend_error"
)

// ToolError describes a memory tool failure in a stable JSON shape.
type ToolError struct {
	Operation string `json:"operation" jsonschema:"tool name that produced the error"`
	Code      string `json:"code" jsonschema:"stable machine-readable error code"`
	Message   string `json:"message" jsonschema:"human-readable error message"`
}

// ToolOutcome reports whether a memory tool succeeded.
type ToolOutcome struct {
	OK    bool       `json:"ok" jsonschema:"true when the tool completed successfully"`
	Error *ToolError `json:"error,omitempty" jsonschema:"error details when ok is false"`
}

type rememberInput struct {
	Fact string `json:"fact" jsonschema:"durable fact to store in Balda memory; call only after the user explicitly asks to remember or save it"`
}

type rememberOutput struct {
	ToolOutcome
	Message string `json:"message" jsonschema:"human-readable result"`
}

type readOutput struct {
	ToolOutcome
	Content string `json:"content" jsonschema:"current durable Balda memory content"`
	Found   bool   `json:"found" jsonschema:"true when durable Balda memory contains non-empty content"`
}

type service struct {
	store *Store
}

// RegisterTools registers Balda memory MCP tools when memory is enabled.
func RegisterTools(server *mcp.Server, store *Store) {
	if server == nil || store == nil || !store.MemoryEnabled() {
		return
	}
	svc := &service{store: store}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "balda.memory.remember",
		Description: "Append a durable fact to Balda memory. Use only when the user explicitly asks you to remember or save a fact. The new fact is available to active sessions on their next turn.",
	}, svc.remember)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "balda.memory.read",
		Description: "Read the current durable Balda memory facts.",
	}, svc.read)
}

func (s *service) remember(ctx context.Context, _ *mcp.CallToolRequest, in rememberInput) (*mcp.CallToolResult, rememberOutput, error) {
	fact := strings.TrimSpace(in.Fact)
	if fact == "" {
		result, out := validationFailure("balda.memory.remember", "fact is required")
		return result, rememberOutput{ToolOutcome: out}, nil
	}
	if _, err := s.store.Remember(ctx, fact); err != nil {
		result, out := backendFailure("balda.memory.remember", err)
		return result, rememberOutput{ToolOutcome: out}, nil
	}
	return nil, rememberOutput{
		ToolOutcome: okOutcome(),
		Message:     "Saved to durable Balda memory. Active sessions will see it on their next turn.",
	}, nil
}

func (s *service) read(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, readOutput, error) {
	content, err := s.store.ReadMemory(ctx)
	if err != nil {
		result, out := backendFailure("balda.memory.read", err)
		return result, readOutput{ToolOutcome: out}, nil
	}
	content = strings.TrimSpace(content)
	return nil, readOutput{
		ToolOutcome: okOutcome(),
		Content:     content,
		Found:       content != "",
	}, nil
}

func okOutcome() ToolOutcome {
	return ToolOutcome{OK: true}
}

func validationFailure(operation string, message string) (*mcp.CallToolResult, ToolOutcome) {
	return failure(operation, codeValidationError, message)
}

func backendFailure(operation string, err error) (*mcp.CallToolResult, ToolOutcome) {
	return failure(operation, codeBackendError, err.Error())
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
				Message:   fmt.Sprintf("%s: %s", operation, message),
			},
		}
}
