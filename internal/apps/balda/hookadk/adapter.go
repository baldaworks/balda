// Package hookadk adapts Google ADK tool callbacks to Balda's normalized hook
// protocol.
package hookadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/tool"
)

const (
	pluginName       = "balda.tool-hooks"
	sourceADK        = "adk"
	maxMessageBytes  = 1024
	deniedCode       = "balda_hook_denied"
	unavailableCode  = "balda_hook_unavailable"
	postFailureCode  = "balda_post_hook_unavailable"
	toolFailureCode  = "tool_error"
	hookContextField = "balda_hook_context"
)

// Runner executes normalized hooks and releases completed invocation state.
type Runner interface {
	Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
	ReleaseInvocation(sessionID, invocationID string)
}

// Scope is immutable context attached to one runner's tool events. Provider is
// opaque to this adapter.
type Scope struct {
	Provider string
	CWD      string
}

// Adapter translates ADK callbacks without depending on provider behavior.
type Adapter struct {
	runner Runner
}

// NewAdapter creates an ADK callback adapter.
func NewAdapter(runner Runner) *Adapter {
	return &Adapter{runner: runner}
}

// NewPlugin creates a fresh host plugin for one ADK runner.
func (a *Adapter) NewPlugin(scope Scope) (*plugin.Plugin, error) {
	if a == nil || a.runner == nil {
		return nil, fmt.Errorf("creating ADK hook plugin: runner is unavailable")
	}
	cwd := filepath.Clean(strings.TrimSpace(scope.CWD))
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("creating ADK hook plugin: working directory must be absolute")
	}
	scope.CWD = cwd
	shortCircuits := newShortCircuitState()
	return plugin.New(plugin.Config{
		Name:                pluginName,
		BeforeToolCallback:  a.beforeTool(scope, shortCircuits),
		OnToolErrorCallback: a.onToolError(scope),
		AfterToolCallback:   a.afterTool(scope, shortCircuits),
		AfterRunCallback: func(ctx adkagent.InvocationContext) {
			shortCircuits.releaseInvocation(ctx.Session().ID(), ctx.InvocationID())
			a.runner.ReleaseInvocation(ctx.Session().ID(), ctx.InvocationID())
		},
	})
}

func (a *Adapter) beforeTool(scope Scope, shortCircuits *shortCircuitState) func(adkagent.Context, tool.Tool, map[string]any) (map[string]any, error) {
	return func(ctx adkagent.Context, called tool.Tool, args map[string]any) (map[string]any, error) {
		input, err := marshalObject(args)
		if err != nil {
			shortCircuits.mark(ctx)
			return failureResult(unavailableCode, "Tool call blocked because hook policy was unavailable."), nil
		}
		result, err := a.runner.Run(ctx, event(scope, ctx, called.Name(), hookcmd.PreToolUse, input, nil))
		if err != nil {
			shortCircuits.mark(ctx)
			return beforeFailure(err), nil
		}
		if len(result.UpdatedInput) == 0 {
			return nil, nil
		}
		var replacement map[string]any
		if err := json.Unmarshal(result.UpdatedInput, &replacement); err != nil || replacement == nil {
			shortCircuits.mark(ctx)
			return failureResult(unavailableCode, "Tool call blocked because hook policy was unavailable."), nil
		}
		clear(args)
		for key, value := range replacement {
			args[key] = value
		}
		return nil, nil
	}
}

func (a *Adapter) onToolError(scope Scope) func(adkagent.Context, tool.Tool, map[string]any, error) (map[string]any, error) {
	return func(ctx adkagent.Context, called tool.Tool, args map[string]any, toolErr error) (map[string]any, error) {
		base := failureResult(toolFailureCode, boundedMessage(toolErr.Error()))
		return a.runPost(scope, ctx, called, args, base)
	}
}

func (a *Adapter) afterTool(scope Scope, shortCircuits *shortCircuitState) func(adkagent.Context, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
	return func(ctx adkagent.Context, called tool.Tool, args, result map[string]any, toolErr error) (map[string]any, error) {
		if shortCircuits.take(ctx) {
			// ADK invokes AfterTool even when BeforeTool returned a result. Return
			// that result unchanged so neither post hooks nor agent callbacks can
			// turn a denied call into an apparent tool execution.
			return result, toolErr
		}
		if toolErr != nil {
			result = failureResult(toolFailureCode, boundedMessage(toolErr.Error()))
		}
		mapped, err := a.runPost(scope, ctx, called, args, result)
		if mapped == nil && err == nil && toolErr != nil {
			// Caller error callbacks already had an opportunity to handle the
			// failure. Normalize only an error that remains afterward.
			return result, nil
		}
		return mapped, err
	}
}

type shortCircuitKey struct {
	sessionID    string
	invocationID string
	toolCallID   string
}

type shortCircuitState struct {
	mu      sync.Mutex
	entries map[shortCircuitKey]struct{}
}

func newShortCircuitState() *shortCircuitState {
	return &shortCircuitState{entries: make(map[shortCircuitKey]struct{})}
}

func (s *shortCircuitState) mark(ctx adkagent.Context) {
	key := shortCircuitKeyFrom(ctx)
	s.mu.Lock()
	s.entries[key] = struct{}{}
	s.mu.Unlock()
}

func (s *shortCircuitState) take(ctx adkagent.Context) bool {
	key := shortCircuitKeyFrom(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return false
	}
	delete(s.entries, key)
	return true
}

func (s *shortCircuitState) releaseInvocation(sessionID, invocationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.entries {
		if key.sessionID == sessionID && key.invocationID == invocationID {
			delete(s.entries, key)
		}
	}
}

func shortCircuitKeyFrom(ctx adkagent.Context) shortCircuitKey {
	return shortCircuitKey{
		sessionID:    ctx.SessionID(),
		invocationID: ctx.InvocationID(),
		toolCallID:   ctx.FunctionCallID(),
	}
}

func (a *Adapter) runPost(scope Scope, ctx adkagent.Context, called tool.Tool, args, output map[string]any) (map[string]any, error) {
	inputJSON, err := marshalObject(args)
	if err != nil {
		return failureResult(postFailureCode, "Tool completed, but post-tool policy was unavailable."), nil
	}
	outputJSON, err := marshalObject(output)
	if err != nil {
		return failureResult(postFailureCode, "Tool completed, but post-tool policy was unavailable."), nil
	}
	result, err := a.runner.Run(ctx, event(scope, ctx, called.Name(), hookcmd.PostToolUse, inputJSON, outputJSON))
	if err != nil {
		return postFailure(err), nil
	}
	var replacement map[string]any
	if len(result.UpdatedOutput) != 0 {
		if err := json.Unmarshal(result.UpdatedOutput, &replacement); err != nil || replacement == nil {
			return failureResult(postFailureCode, "Tool completed, but post-tool policy was unavailable."), nil
		}
	}
	if result.AdditionalContext != "" {
		if replacement == nil {
			replacement = cloneMap(output)
		}
		replacement[hookContextField] = boundedMessage(result.AdditionalContext)
	}
	return replacement, nil
}

func event(scope Scope, ctx adkagent.Context, toolName string, name hookcmd.EventName, input, output json.RawMessage) hookcmd.Event {
	return hookcmd.Event{
		Name:           name,
		SessionID:      ctx.SessionID(),
		AgentSessionID: ctx.SessionID(),
		InvocationID:   ctx.InvocationID(),
		Provider:       scope.Provider,
		CWD:            scope.CWD,
		Source:         sourceADK,
		ToolName:       toolName,
		ToolCallID:     ctx.FunctionCallID(),
		ToolInput:      input,
		ToolResponse:   output,
	}
}

func beforeFailure(err error) map[string]any {
	var denied *hookcmd.DeniedError
	if errors.As(err, &denied) {
		message := strings.TrimSpace(denied.Reason)
		if message == "" {
			message = "Tool call denied by hook policy."
		}
		return failureResult(deniedCode, boundedMessage(message))
	}
	return failureResult(unavailableCode, "Tool call blocked because hook policy was unavailable.")
}

func postFailure(err error) map[string]any {
	var denied *hookcmd.DeniedError
	if errors.As(err, &denied) {
		return failureResult(postFailureCode, "Tool completed, but post-tool policy rejected its response.")
	}
	return failureResult(postFailureCode, "Tool completed, but post-tool policy was unavailable.")
}

func failureResult(code, message string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": boundedMessage(message)}}
}

func marshalObject(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func cloneMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value)+1)
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func boundedMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxMessageBytes {
		return message
	}
	return message[:maxMessageBytes]
}
