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

type ToolError struct {
	Operation string `json:"operation" jsonschema:"tool name that produced the error"`
	Code      string `json:"code" jsonschema:"stable machine-readable error code"`
	Message   string `json:"message" jsonschema:"human-readable error message"`
}

type ToolOutcome struct {
	OK    bool       `json:"ok" jsonschema:"true when the tool completed successfully"`
	Error *ToolError `json:"error,omitempty" jsonschema:"error details when ok is false"`
}

type rememberInput struct {
	Fact string `json:"fact" jsonschema:"durable fact to append to MEMORY.md; call only after the user explicitly asks to remember or save it"`
}

type rememberOutput struct {
	ToolOutcome
	Message string `json:"message" jsonschema:"human-readable result"`
}

type readOutput struct {
	ToolOutcome
	Content string `json:"content" jsonschema:"current MEMORY.md content"`
	Found   bool   `json:"found" jsonschema:"true when MEMORY.md contains non-empty content"`
}

type service struct {
	store *Store
}

func RegisterTools(server *mcp.Server, store *Store) {
	if server == nil || store == nil || !store.Enabled() {
		return
	}
	svc := &service{store: store}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "relay.memory.remember",
		Description: "Append a durable fact to MEMORY.md. Use only when the user explicitly asks you to remember or save a fact. The new fact applies to future Relay sessions.",
	}, svc.remember)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "relay.memory.read",
		Description: "Read the current MEMORY.md durable facts.",
	}, svc.read)
}

func (s *service) remember(ctx context.Context, _ *mcp.CallToolRequest, in rememberInput) (*mcp.CallToolResult, rememberOutput, error) {
	fact := strings.TrimSpace(in.Fact)
	if fact == "" {
		result, out := validationFailure("relay.memory.remember", "fact is required")
		return result, rememberOutput{ToolOutcome: out}, nil
	}
	if err := s.store.Remember(ctx, fact); err != nil {
		result, out := backendFailure("relay.memory.remember", err)
		return result, rememberOutput{ToolOutcome: out}, nil
	}
	return nil, rememberOutput{
		ToolOutcome: okOutcome(),
		Message:     "Saved to MEMORY.md. The fact will be injected into future sessions after they start or restore.",
	}, nil
}

func (s *service) read(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, readOutput, error) {
	content, err := s.store.ReadMemory(ctx)
	if err != nil {
		result, out := backendFailure("relay.memory.read", err)
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
