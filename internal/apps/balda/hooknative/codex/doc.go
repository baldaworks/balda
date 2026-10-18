// Package codex owns Codex native hook launch and wire adaptation.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativehost"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

const name = "codex"

type Adapter struct{ host hooknativehost.Host }

func New(host hooknativehost.Host) hooknative.Adapter { return &Adapter{host: host} }
func (*Adapter) Name() string                         { return name }
func (*Adapter) Supports(c agentconfig.Config) bool   { return c.Type == agentconfig.AgentTypeCodexACP }
func (a *Adapter) Prepare(ctx context.Context, r hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	if r.SessionID == "" || strings.TrimSpace(r.CWD) == "" || r.LeafID == "" {
		return hooknative.PreparedLeaf{}, hookcmd.ErrInvalid
	}
	if !hookprovider.NativePlatformSupported() {
		return hooknative.PreparedLeaf{}, hookcmd.ErrUnsupported
	}
	resolved, err := agentconfig.NormalizeConfig(r.Config, a.host.Executable())
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	b, err := a.host.Bind(ctx, hookprovider.BindingScope{SessionID: r.SessionID, Provider: r.ProviderID, CWD: r.CWD})
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	if err := hooknativehost.WriteMetadata(b, name); err != nil {
		_ = b.Release()
		return hooknative.PreparedLeaf{}, err
	}
	return hooknative.PreparedLeaf{Config: hooknativehost.WrapLaunch(r.Config, a.host.Executable(), name, b, resolved), Release: b.Release}, nil
}

type Protocol struct{}

func (Protocol) Name() string { return name }
func (Protocol) Dispatch(ctx context.Context, b hookprovider.Binding, in io.Reader, out io.Writer) error {
	return dispatch(ctx, b, in, out)
}
func (Protocol) Proxy(ctx context.Context, b hookprovider.Binding, path string, argv []string, in io.Reader, out, stderr io.Writer) error {
	return proxyruntime.Proxy(ctx, b, path, argv, in, out, stderr, behavior{})
}

type behavior struct{}

func (behavior) Prepare(ctx context.Context, b hookprovider.Binding, _ string, argv []string, _ map[string]string) ([]string, error) {
	return prepareCodex(ctx, argv, func(pid int) (func(), error) { return b.RegisterProcess(ctx, pid) })
}
func (behavior) RewriteRequest(line []byte) ([]byte, error) { return line, nil }
func (behavior) CheckResponse(line []byte) error            { return checkVersion(line) }
func nativeCommand() string {
	if runtime.GOOS == "windows" {
		return `"%BALDA_HOOK_EXECUTABLE%" agent-hooks dispatch codex`
	}
	return `"${BALDA_HOOK_EXECUTABLE}" agent-hooks dispatch codex`
}
func nativeReadyCommand() string {
	if runtime.GOOS == "windows" {
		return `"%BALDA_HOOK_EXECUTABLE%" agent-hooks ready codex`
	}
	return `"${BALDA_HOOK_EXECUTABLE}" agent-hooks ready codex`
}
func encode(v any) json.RawMessage { data, _ := json.Marshal(v); return data }
func checkVersion(line []byte) error {
	var m struct {
		Result struct {
			AgentInfo json.RawMessage `json:"agentInfo"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &m) != nil {
		return hookcmd.ErrInvalid
	}
	if len(m.Result.AgentInfo) == 0 {
		return nil
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(m.Result.AgentInfo, &info) != nil {
		return hookcmd.ErrUnsupported
	}
	minimum := []int{1, 10, 1}
	parts := strings.Split(strings.TrimPrefix(strings.Split(info.Version, "-")[0], "v"), ".")
	if len(parts) != 3 {
		return hookcmd.ErrUnsupported
	}
	for i, p := range parts {
		n, e := strconv.Atoi(p)
		if e != nil {
			return hookcmd.ErrUnsupported
		}
		if n > minimum[i] {
			return nil
		}
		if n < minimum[i] {
			return hookcmd.ErrUnsupported
		}
	}
	return nil
}
func dispatch(ctx context.Context, b hookprovider.Binding, input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, 256<<10+1))
	var n struct {
		Event        hookcmd.EventName `json:"hook_event_name"`
		SessionID    string            `json:"session_id"`
		ToolName     string            `json:"tool_name"`
		ToolCallID   string            `json:"tool_use_id"`
		ToolInput    json.RawMessage   `json:"tool_input"`
		ToolResponse json.RawMessage   `json:"tool_response"`
	}
	if err != nil || len(data) > 256<<10 || json.Unmarshal(data, &n) != nil || !n.Event.ToolEvent() {
		return hookcmd.ErrInvalid
	}
	result, err := b.Invoke(ctx, hookcmd.Event{Name: n.Event, AgentSessionID: n.SessionID, ToolName: n.ToolName, ToolCallID: n.ToolCallID, ToolInput: n.ToolInput, ToolResponse: n.ToolResponse})
	if err != nil {
		return err
	}
	specific := map[string]any{"hookEventName": n.Event}
	envelope := map[string]any{"hookSpecificOutput": specific}
	if result.AdditionalContext != "" {
		specific["additionalContext"] = result.AdditionalContext
	}
	if n.Event == hookcmd.PreToolUse {
		if result.Decision == hookcmd.DecisionDeny {
			specific["permissionDecision"] = "deny"
			specific["permissionDecisionReason"] = reason(result.Reason)
		} else if len(result.UpdatedInput) != 0 {
			specific["permissionDecision"] = "allow"
			specific["updatedInput"] = result.UpdatedInput
		}
	} else if result.Decision == hookcmd.DecisionDeny {
		envelope["decision"] = "block"
		envelope["reason"] = reason(result.Reason)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(envelope); err != nil {
		return err
	}
	_, err = io.Copy(output, &buf)
	return err
}
func reason(v string) string {
	if strings.TrimSpace(v) == "" {
		return "Balda hook rejected the tool event."
	}
	return v
}
