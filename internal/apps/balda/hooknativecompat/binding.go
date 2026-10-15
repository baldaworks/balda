// Package hooknativecompat preserves the pre-registry native launch path while
// provider adapters move to their final packages.
package hooknativecompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

const (
	bindingEnv     = "BALDA_HOOK_BINDING"
	executableEnv  = "BALDA_HOOK_EXECUTABLE"
	kindClaude     = "claude"
	kindCodex      = "codex"
	kindOpenCode   = "opencode"
	maxNativeBytes = 256 << 10
)

type binding struct {
	hookprovider.Binding
	Kind string
}

func readBinding(path string) (binding, error) {
	neutral, err := hookprovider.ReadBinding(path)
	if err != nil {
		return binding{}, err
	}
	kindBytes, err := os.ReadFile(path + ".kind")
	if err != nil {
		return binding{}, hookcmd.ErrUnavailable
	}
	kind := string(kindBytes)
	if kind != kindClaude && kind != kindCodex && kind != kindOpenCode {
		return binding{}, hookcmd.ErrUnavailable
	}
	return binding{Binding: neutral, Kind: kind}, nil
}

func readOwnerProof(path string) (string, error) {
	proof, err := os.ReadFile(path + ".owner-proof")
	if err != nil || len(proof) != 64 {
		return "", hookcmd.ErrUnavailable
	}
	return string(proof), nil
}

type nativeInput struct {
	Event        hookcmd.EventName `json:"hook_event_name"`
	SessionID    string            `json:"session_id"`
	ToolName     string            `json:"tool_name"`
	ToolCallID   string            `json:"tool_use_id"`
	ToolInput    json.RawMessage   `json:"tool_input"`
	ToolResponse json.RawMessage   `json:"tool_response"`
}

// Dispatch translates the legacy native command protocol into normalized events.
func Dispatch(ctx context.Context, path string, input io.Reader, output io.Writer) error {
	binding, err := readBinding(path)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, maxNativeBytes+1))
	var native nativeInput
	if err != nil || len(data) > maxNativeBytes || json.Unmarshal(data, &native) != nil || !native.Event.ToolEvent() {
		return hookcmd.ErrInvalid
	}
	result, err := binding.Invoke(ctx, hookcmd.Event{Name: native.Event, AgentSessionID: native.SessionID, ToolName: native.ToolName, ToolCallID: native.ToolCallID, ToolInput: native.ToolInput, ToolResponse: native.ToolResponse})
	if err != nil {
		return err
	}
	specific := map[string]any{"hookEventName": native.Event}
	envelope := map[string]any{"hookSpecificOutput": specific}
	if result.AdditionalContext != "" {
		specific["additionalContext"] = result.AdditionalContext
	}
	if native.Event == hookcmd.PreToolUse {
		if result.Decision == hookcmd.DecisionDeny {
			specific["permissionDecision"], specific["permissionDecisionReason"] = "deny", denialReason(result.Reason)
		} else if len(result.UpdatedInput) != 0 {
			decision := "allow"
			if binding.Kind == kindClaude {
				decision = "ask"
			}
			specific["permissionDecision"], specific["updatedInput"] = decision, result.UpdatedInput
		}
	} else if result.Decision == hookcmd.DecisionDeny {
		envelope["decision"], envelope["reason"] = "block", denialReason(result.Reason)
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

func denialReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Balda hook rejected the tool event."
	}
	return reason
}

// RunCommandMode handles transitional native adapter commands before startup.
func RunCommandMode(args []string) (bool, int) {
	if len(args) == 0 || args[0] != "agent-hooks" {
		return false, 0
	}
	var err error
	switch {
	case len(args) == 2 && args[1] == "ready":
		var binding binding
		binding, err = readBinding(os.Getenv(bindingEnv))
		if err == nil {
			err = binding.SignalReady(context.Background())
		}
		if err == nil {
			_, err = io.WriteString(os.Stdout, "{}\n")
		}
	case len(args) == 2 && args[1] == "dispatch":
		err = Dispatch(context.Background(), os.Getenv(bindingEnv), os.Stdin, os.Stdout)
	case len(args) >= 6 && args[1] == "proxy" && args[4] == "--":
		var proof string
		proof, err = readOwnerProof(args[2])
		if err == nil && proof != args[3] {
			err = hookcmd.ErrUnavailable
		}
		if err == nil {
			err = Proxy(context.Background(), args[2], args[5:], os.Stdin, os.Stdout, os.Stderr)
		}
	default:
		err = hookcmd.ErrInvalid
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Balda tool hook unavailable; invocation rejected.")
		return true, 2
	}
	return true, 0
}

func invoke(ctx context.Context, binding binding, event hookcmd.Event) (hookcmd.Result, error) {
	return binding.Invoke(ctx, event)
}
