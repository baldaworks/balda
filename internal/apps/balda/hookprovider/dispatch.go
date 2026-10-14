package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

const bindingEnv = "BALDA_HOOK_BINDING"
const executableEnv = "BALDA_HOOK_EXECUTABLE"

func readBinding(path string) (Binding, error) {
	var binding Binding
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return binding, hookcmd.ErrUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &binding) != nil {
		return binding, hookcmd.ErrUnavailable
	}
	parsed, err := url.Parse(binding.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "/invoke" || parsed.User != nil || parsed.RawQuery != "" || len(binding.Token) != 64 {
		return Binding{}, hookcmd.ErrUnavailable
	}
	switch binding.Kind {
	case kindClaude, kindCodex, kindOpenCode:
	default:
		return Binding{}, hookcmd.ErrUnavailable
	}
	return binding, nil
}

type nativeInput struct {
	Event        hookcmd.EventName `json:"hook_event_name"`
	SessionID    string            `json:"session_id"`
	ToolName     string            `json:"tool_name"`
	ToolCallID   string            `json:"tool_use_id"`
	ToolInput    json.RawMessage   `json:"tool_input"`
	ToolResponse json.RawMessage   `json:"tool_response"`
}

func invoke(ctx context.Context, binding Binding, event hookcmd.Event) (hookcmd.Result, error) {
	var result hookcmd.Result
	data, err := json.Marshal(event)
	if err != nil || len(data) > maxNativeBytes {
		return result, hookcmd.ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, binding.URL, bytes.NewReader(data))
	if err != nil {
		return result, hookcmd.ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+binding.Token)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 125 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return result, hookcmd.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	data, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || response.StatusCode != http.StatusOK {
		return result, hookcmd.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return result, hookcmd.ErrUnavailable
	}
	return result, nil
}

// Dispatch translates native stdin and results without opening a Balda database.
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
	result, err := invoke(ctx, binding, hookcmd.Event{Name: native.Event, AgentSessionID: native.SessionID, ToolName: native.ToolName, ToolCallID: native.ToolCallID, ToolInput: native.ToolInput, ToolResponse: native.ToolResponse})
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
			// Codex applies rewrites only with allow. Claude's ask keeps its
			// permission callback responsible for the rewritten invocation.
			decision := "allow"
			if binding.Kind == kindClaude {
				decision = "ask"
			}
			specific["permissionDecision"], specific["updatedInput"] = decision, result.UpdatedInput
		}
	} else if result.Decision == hookcmd.DecisionDeny {
		envelope["decision"], envelope["reason"] = "block", denialReason(result.Reason)
	}
	return json.NewEncoder(output).Encode(envelope)
}

func denialReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Balda hook rejected the tool event."
	}
	return reason
}

// RunCommandMode handles private adapter commands before ordinary app startup.
func RunCommandMode(args []string) (bool, int) {
	if len(args) == 0 || args[0] != "agent-hooks" {
		return false, 0
	}
	var err error
	switch {
	case len(args) == 2 && args[1] == "ready":
		var binding Binding
		binding, err = readBinding(os.Getenv(bindingEnv))
		if err == nil {
			err = sendReady(context.Background(), binding, false)
		}
		if err == nil {
			_, err = io.WriteString(os.Stdout, "{}\n")
		}
	case len(args) == 2 && args[1] == "dispatch":
		err = Dispatch(context.Background(), os.Getenv(bindingEnv), os.Stdin, os.Stdout)
	case len(args) >= 5 && args[1] == "proxy" && args[3] == "--":
		err = Proxy(context.Background(), args[2], args[4:], os.Stdin, os.Stdout, os.Stderr)
	default:
		err = hookcmd.ErrInvalid
	}
	if err != nil {
		// Exit 2 with a nonempty reason is the native fail-closed contract.
		_, _ = fmt.Fprintln(os.Stderr, "Balda tool hook unavailable; invocation rejected.")
		return true, 2
	}
	return true, 0
}
