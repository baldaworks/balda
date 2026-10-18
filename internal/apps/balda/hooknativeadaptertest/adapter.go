package hooknativeadaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativehost"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

// RealAdapterFactory creates contract fixtures around a concrete adapter and protocol.
func RealAdapterFactory(adapterFactory func(hooknativehost.Host) hooknative.Adapter, protocol proxyruntime.Protocol, providerType string, direct bool) hooknativetest.AdapterFactory {
	return func(t testing.TB, launch hooknativetest.LaunchFixture, runner hooknativetest.Runner) hooknativetest.Adapter {
		t.Helper()
		service, err := hookprovider.New(runnerAdapter{runner})
		if err != nil {
			t.Fatal(err)
		}
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		host := testHost{service: service, executable: executable}
		config := agentconfig.Config{Type: providerType}
		acp := &agentconfig.ACPConfig{Cmd: append([]string(nil), launch.Command...), Model: launch.Model, Mode: launch.Mode}
		switch providerType {
		case agentconfig.AgentTypeClaudeCodeACP:
			config.ClaudeCodeACP = acp
		case agentconfig.AgentTypeCodexACP:
			config.CodexACP = acp
		case agentconfig.AgentTypeOpenCodeACP:
			config.OpenCodeACP = acp
		}
		prepared, err := adapterFactory(host).Prepare(context.Background(), hooknative.LeafRequest{SessionID: "session", ProviderID: "provider", LeafID: "leaf", CWD: t.TempDir(), Config: config})
		if err != nil {
			_ = service.Close()
			t.Fatal(err)
		}
		command := prepared.Config.GenericACP.Cmd
		binding, err := hookprovider.ReadBinding(command[4])
		if err != nil {
			_ = prepared.Release()
			_ = service.Close()
			t.Fatal(err)
		}
		return &realAdapter{launch: hooknativetest.PreparedLaunch{Command: command, Model: prepared.Config.GenericACP.Model, Mode: prepared.Config.GenericACP.Mode, BindingReference: command[4]}, binding: binding, protocol: protocol, direct: direct, release: prepared.Release, service: service}
	}
}

type runnerAdapter struct{ runner hooknativetest.Runner }

func (r runnerAdapter) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return r.runner(ctx, event)
}

type testHost struct {
	service    *hookprovider.Service
	executable string
}

func (h testHost) Bind(ctx context.Context, scope hookprovider.BindingScope) (*hookprovider.Binding, error) {
	return h.service.Bind(ctx, scope)
}
func (h testHost) Executable() string { return h.executable }

type realAdapter struct {
	launch   hooknativetest.PreparedLaunch
	binding  hookprovider.Binding
	protocol proxyruntime.Protocol
	direct   bool
	release  func() error
	service  *hookprovider.Service
}

func (a *realAdapter) Launch() hooknativetest.PreparedLaunch { return a.launch }
func (a *realAdapter) RunTool(ctx context.Context, call hooknativetest.ToolCall) (hooknativetest.ToolObservation, error) {
	event := hookcmd.Event{Name: hookcmd.PreToolUse, ToolName: call.Name, ToolCallID: call.CallID, ToolInput: call.Input}
	pre, err := a.invoke(ctx, event)
	if err != nil {
		return hooknativetest.ToolObservation{}, err
	}
	obs := hooknativetest.ToolObservation{Executed: pre.Decision != hookcmd.DecisionDeny, EffectiveInput: call.Input, Feedback: pre.Reason}
	if len(pre.UpdatedInput) != 0 {
		obs.EffectiveInput = pre.UpdatedInput
	}
	if !obs.Executed {
		return obs, nil
	}
	post, err := a.invoke(ctx, hookcmd.Event{Name: hookcmd.PostToolUse, ToolName: call.Name, ToolCallID: call.CallID, ToolInput: obs.EffectiveInput, ToolResponse: call.Response})
	if err != nil {
		return hooknativetest.ToolObservation{}, err
	}
	obs.PostContext = post.AdditionalContext
	return obs, nil
}
func (a *realAdapter) invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if a.direct {
		return a.binding.Invoke(ctx, event)
	}
	payload, _ := json.Marshal(map[string]any{"hook_event_name": event.Name, "session_id": "agent-session", "tool_name": event.ToolName, "tool_use_id": event.ToolCallID, "tool_input": event.ToolInput, "tool_response": event.ToolResponse})
	var output bytes.Buffer
	if err := a.protocol.Dispatch(ctx, a.binding, bytes.NewReader(payload), &output); err != nil {
		return hookcmd.Result{}, err
	}
	var envelope struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		Specific struct {
			Permission string          `json:"permissionDecision"`
			Reason     string          `json:"permissionDecisionReason"`
			Updated    json.RawMessage `json:"updatedInput"`
			Context    string          `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(output.Bytes(), &envelope) != nil {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	result := hookcmd.Result{AdditionalContext: envelope.Specific.Context, UpdatedInput: envelope.Specific.Updated}
	if envelope.Specific.Permission == "deny" {
		result.Decision = hookcmd.DecisionDeny
		result.Reason = envelope.Specific.Reason
	}
	if envelope.Decision == "block" {
		result.Decision = hookcmd.DecisionDeny
		result.Reason = envelope.Reason
	}
	return result, nil
}
func (a *realAdapter) Close() error {
	err := a.release()
	if closeErr := a.service.Close(); err == nil {
		err = closeErr
	}
	return err
}
