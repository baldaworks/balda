package actors

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type activationRegistryFixture struct {
	types []deliveryfmt.MessageType
	err   error
}

func (f *activationRegistryFixture) RenderStructured(_ context.Context, _ string, typ deliveryfmt.MessageType, _ any) (deliveryfmt.StructuredPresentation, error) {
	f.types = append(f.types, typ)
	if f.err != nil {
		return deliveryfmt.StructuredPresentation{}, f.err
	}
	return deliveryfmt.StructuredPresentation{Text: "bounded presentation", DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}, nil
}

type activationDispatcherFixture struct {
	attempts []actorlayer.Envelope
	err      error
}

func (f *activationDispatcherFixture) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.attempts = append(f.attempts, env)
	if f.err != nil {
		return nil, f.err
	}
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

type activationRunnerFixture struct{ calls int }

func (f *activationRunnerFixture) RunSessionTurnPayload(context.Context, SessionTurnPayload) error {
	f.calls++
	return nil
}

func TestSessionActorActivationPolicy(t *testing.T) {
	tests := []struct {
		name      string
		kind      sessioncmd.ActivationKind
		warnings  []sessioncmd.WarningCode
		wantTypes []deliveryfmt.MessageType
	}{
		{name: "created", kind: sessioncmd.ActivationCreated, wantTypes: []deliveryfmt.MessageType{sessionfmt.NoticeDescriptor.Type}},
		{name: "restored silent", kind: sessioncmd.ActivationRestored},
		{name: "replaced", kind: sessioncmd.ActivationReplaced, wantTypes: []deliveryfmt.MessageType{sessionfmt.NoticeDescriptor.Type}},
		{name: "restored warning", kind: sessioncmd.ActivationRestored, warnings: []sessioncmd.WarningCode{sessioncmd.WarningWorkspaceSyncSkipped}, wantTypes: []deliveryfmt.MessageType{sessionfmt.WarningDescriptor.Type}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &activationRegistryFixture{}
			dispatcher := &activationDispatcherFixture{}
			runner := &activationRunnerFixture{}
			actor := NewSessionActor(SessionActorConfig{Turns: immediateTurnQueue{}, Runner: runner, Registry: registry, Dispatcher: dispatcher, Logger: zerolog.Nop()})
			if err := actor.Handle(t.Context(), activationTurnEnvelope(t, activationFixture(test.kind, test.warnings...))); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || len(registry.types) != len(test.wantTypes) || len(dispatcher.attempts) != len(test.wantTypes) {
				t.Fatalf("runner=%d rendered=%v deliveries=%d", runner.calls, registry.types, len(dispatcher.attempts))
			}
			for i, typ := range test.wantTypes {
				if registry.types[i] != typ {
					t.Fatalf("rendered types = %v, want %v", registry.types, test.wantTypes)
				}
				var payload deliverycmd.Payload
				if err := actorlayer.UnmarshalPayload(dispatcher.attempts[i].Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Settlement != deliverycmd.SettlementBypass {
					t.Fatalf("settlement = %q, want bypass", payload.Settlement)
				}
			}
		})
	}
}

func TestSessionActorActivationPresentationFailuresAreAdvisory(t *testing.T) {
	for name, registryErr := range map[string]error{"render": errors.New("render failed"), "dispatch": nil} {
		t.Run(name, func(t *testing.T) {
			registry := &activationRegistryFixture{err: registryErr}
			dispatcher := &activationDispatcherFixture{}
			if name == "dispatch" {
				dispatcher.err = errors.New("dispatch failed")
			}
			runner := &activationRunnerFixture{}
			actor := NewSessionActor(SessionActorConfig{Turns: immediateTurnQueue{}, Runner: runner, Registry: registry, Dispatcher: dispatcher, Logger: zerolog.Nop()})
			if err := actor.Handle(t.Context(), activationTurnEnvelope(t, activationFixture(sessioncmd.ActivationCreated))); err != nil {
				t.Fatalf("activation presentation changed turn outcome: %v", err)
			}
			if runner.calls != 1 {
				t.Fatalf("runner calls = %d", runner.calls)
			}
		})
	}
}

const activationTestSessionID = "tg-9001-0"

func activationFixture(kind sessioncmd.ActivationKind, warnings ...sessioncmd.WarningCode) *sessioncmd.Activation {
	return &sessioncmd.Activation{
		Kind: kind, SessionID: activationTestSessionID, Label: "balda",
		Runtime:  sessioncmd.RuntimeSummary{PublicRuntimeMetadata: sessioncmd.PublicRuntimeMetadata{ProviderID: "codex", MCPServers: []string{"balda"}}, EffectiveModel: "gpt"},
		Warnings: append([]sessioncmd.WarningCode(nil), warnings...),
	}
}

func activationTurnEnvelope(t *testing.T, activation *sessioncmd.Activation) actorlayer.Envelope {
	t.Helper()
	env, err := turncmd.SessionTurnEnvelope(SessionTurnPayload{
		Activation: activation, Text: "continue", Source: turncmd.SourceTelegram, DedupeKey: "telegram:message:1", Deliver: true,
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: activationTestSessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}
