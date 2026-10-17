// Package claude owns Claude native hook launch and wire adaptation.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

const name = "claude"
const envelopeSeconds = 2147483

type Adapter struct{ host hooknativehost.Host }

func New(host hooknativehost.Host) hooknative.Adapter { return &Adapter{host: host} }
func (*Adapter) Name() string                         { return name }
func (*Adapter) Supports(config agentconfig.Config) bool {
	return config.Type == agentconfig.AgentTypeClaudeCodeACP || config.Type == agentconfig.AgentTypeClaudeACP
}
func (a *Adapter) Prepare(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	if request.SessionID == "" || strings.TrimSpace(request.CWD) == "" || request.LeafID == "" {
		return hooknative.PreparedLeaf{}, hookcmd.ErrInvalid
	}
	if !hookprovider.NativePlatformSupported() {
		return hooknative.PreparedLeaf{}, hookcmd.ErrUnsupported
	}
	resolved, err := agentconfig.NormalizeConfig(request.Config, a.host.Executable())
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	binding, err := a.host.Bind(ctx, hookprovider.BindingScope{SessionID: request.SessionID, Provider: request.ProviderID, CWD: request.CWD})
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	if err := hooknativehost.WriteMetadata(binding, name); err != nil {
		_ = binding.Release()
		return hooknative.PreparedLeaf{}, err
	}
	return hooknative.PreparedLeaf{Config: hooknativehost.WrapLaunch(request.Config, a.host.Executable(), name, binding, resolved), Release: binding.Release}, nil
}

type Protocol struct{}

func (Protocol) Name() string { return name }
func (Protocol) Dispatch(ctx context.Context, binding hookprovider.Binding, input io.Reader, output io.Writer) error {
	return dispatch(ctx, binding, input, output, true)
}
func (Protocol) Proxy(ctx context.Context, binding hookprovider.Binding, path string, argv []string, input io.Reader, output, stderr io.Writer) error {
	return proxyruntime.Proxy(ctx, binding, path, argv, input, output, stderr, behavior{})
}

type behavior struct{}

func (behavior) Prepare(_ context.Context, _ hookprovider.Binding, _ string, argv []string, _ map[string]string) ([]string, error) {
	return argv, nil
}
func (behavior) RewriteRequest(line []byte) ([]byte, error) { return inject(line) }
func (behavior) CheckResponse(line []byte) error            { return checkVersion(line, []int{0, 16, 2}) }

func nativeCommand() string {
	if runtime.GOOS == "windows" {
		return `"%BALDA_HOOK_EXECUTABLE%" agent-hooks dispatch claude`
	}
	return `"${BALDA_HOOK_EXECUTABLE}" agent-hooks dispatch claude`
}
func readyCommand() string {
	if runtime.GOOS == "windows" {
		return `"%BALDA_HOOK_EXECUTABLE%" agent-hooks ready claude`
	}
	return `"${BALDA_HOOK_EXECUTABLE}" agent-hooks ready claude`
}
func hookGroups() map[string]any {
	group := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": nativeCommand(), "timeout": envelopeSeconds}}}}
	return map[string]any{"PreToolUse": group, "PostToolUse": group, "SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": readyCommand(), "timeout": 10}}}}}
}
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil, hookcmd.ErrInvalid
	}
	return v, nil
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func inject(line []byte) ([]byte, error) {
	m, err := object(line)
	if err != nil {
		return nil, err
	}
	var method string
	_ = json.Unmarshal(m["method"], &method)
	if method != "session/new" && method != "session/load" && method != "session/resume" {
		return line, nil
	}
	p, err := object(m["params"])
	if err != nil {
		return nil, err
	}
	meta, err := object(p["_meta"])
	if err != nil {
		return nil, err
	}
	c, err := object(meta["claudeCode"])
	if err != nil {
		return nil, err
	}
	o, err := object(c["options"])
	if err != nil {
		return nil, err
	}
	extra, err := object(o["extraArgs"])
	if err != nil {
		return nil, err
	}
	raw := o["settings"]
	if len(extra["settings"]) != 0 {
		raw = extra["settings"]
	}
	settings, err := nativeSettings(raw, p["cwd"])
	if err != nil {
		return nil, err
	}
	var disabled bool
	_ = json.Unmarshal(settings["disableAllHooks"], &disabled)
	if disabled {
		return nil, hookcmd.ErrUnsupported
	}
	hooks, err := object(settings["hooks"])
	if err != nil {
		return nil, err
	}
	for n, g := range hookGroups() {
		var existing []json.RawMessage
		if len(hooks[n]) != 0 && json.Unmarshal(hooks[n], &existing) != nil {
			return nil, hookcmd.ErrInvalid
		}
		var add []json.RawMessage
		_ = json.Unmarshal(encode(g), &add)
		hooks[n] = encode(append(existing, add...))
	}
	settings["hooks"] = encode(hooks)
	extra["settings"] = encode(string(encode(settings)))
	o["extraArgs"] = encode(extra)
	delete(o, "settings")
	c["options"] = encode(o)
	meta["claudeCode"] = encode(c)
	p["_meta"] = encode(meta)
	m["params"] = encode(p)
	return json.Marshal(m)
}
func nativeSettings(raw, cwdRaw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '"' {
		return object(raw)
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, hookcmd.ErrInvalid
	}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		return object([]byte(value))
	}
	var cwd string
	_ = json.Unmarshal(cwdRaw, &cwd)
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, hookcmd.ErrUnsupported
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, hookcmd.ErrUnsupported
	}
	return object(data)
}
func checkVersion(line []byte, minimum []int) error {
	m, err := object(line)
	if err != nil {
		return hookcmd.ErrInvalid
	}
	result, err := object(m["result"])
	if err != nil || len(result["agentInfo"]) == 0 {
		return nil
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(result["agentInfo"], &info) != nil {
		return hookcmd.ErrUnsupported
	}
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
func dispatch(ctx context.Context, binding hookprovider.Binding, input io.Reader, output io.Writer, askRewrite bool) error {
	data, err := io.ReadAll(io.LimitReader(input, 256<<10+1))
	var native struct {
		Event        hookcmd.EventName `json:"hook_event_name"`
		SessionID    string            `json:"session_id"`
		ToolName     string            `json:"tool_name"`
		ToolCallID   string            `json:"tool_use_id"`
		ToolInput    json.RawMessage   `json:"tool_input"`
		ToolResponse json.RawMessage   `json:"tool_response"`
	}
	if err != nil || len(data) > 256<<10 || json.Unmarshal(data, &native) != nil || !native.Event.ToolEvent() {
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
			specific["permissionDecision"] = "deny"
			specific["permissionDecisionReason"] = reason(result.Reason)
		} else if len(result.UpdatedInput) != 0 {
			decision := "allow"
			if askRewrite {
				decision = "ask"
			}
			specific["permissionDecision"] = decision
			specific["updatedInput"] = result.UpdatedInput
		}
	} else if result.Decision == hookcmd.DecisionDeny {
		envelope["decision"] = "block"
		envelope["reason"] = reason(result.Reason)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(envelope); err != nil {
		return err
	}
	_, err = io.Copy(output, &out)
	return err
}
func reason(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Balda hook rejected the tool event."
	}
	return value
}
