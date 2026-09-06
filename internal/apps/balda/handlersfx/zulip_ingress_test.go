package handlersfx

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeZulipOwnerKVStore struct{}

func (fakeZulipOwnerKVStore) GetJSON(context.Context, string) (any, bool, error) {
	return nil, false, nil
}
func (fakeZulipOwnerKVStore) SetJSON(context.Context, string, any) error { return nil }
func (fakeZulipOwnerKVStore) SetWithTTL(context.Context, string, any, time.Duration) error {
	return nil
}
func (fakeZulipOwnerKVStore) Delete(context.Context, string) error           { return nil }
func (fakeZulipOwnerKVStore) List(context.Context, string) ([]string, error) { return nil, nil }

type recordingZulipDispatcher struct {
	commands     []actorlayer.Envelope
	err          error
	stateManager interface {
		UpdateRuntimeState(ctx context.Context, locator deliverycmd.Locator, state map[string]any) error
	}
}

type recordingZulipCommandIngress struct{ requests []commandcmd.Request }

func (r *recordingZulipCommandIngress) PublishCommand(_ context.Context, req commandcmd.Request) error {
	r.requests = append(r.requests, req)
	return nil
}

func TestZulipInboundHandlerPublishesActorOwnedCommands(t *testing.T) {
	ownerStore, err := auth.NewOwnerStore(&fakeZulipOwnerKVStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerStore.RegisterOwnerSubject(auth.ZulipSubject(101)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"locator", "reset"} {
		t.Run(name, func(t *testing.T) {
			ingress := &recordingZulipCommandIngress{}
			h := &zulipInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop(), ownerID: 101}
			locator := zulip.NewDMLocator(101)
			if err := h.HandleCommand(context.Background(), zulip.InboundCommand{Locator: locator, MessageID: 88, SenderID: 101, Command: name, Direct: true}); err != nil {
				t.Fatal(err)
			}
			if len(ingress.requests) != 1 {
				t.Fatalf("published requests = %d, want 1", len(ingress.requests))
			}
			got := ingress.requests[0]
			if got.InvocationID != "zulip:command:88" || got.Payload.Name != name || !got.Payload.Access.Owner {
				t.Fatalf("published request = %+v", got)
			}
		})
	}
}

func (d *recordingZulipDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if env.Namespace == baldaexecution.NamespaceAutoModeCommand && d.stateManager != nil {
		var payload automodecmd.Payload
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			return nil, err
		}
		if err := d.stateManager.UpdateRuntimeState(context.Background(), payload.Locator, payload.State); err != nil {
			return nil, err
		}
	}
	d.commands = append(d.commands, env)
	if d.err != nil {
		return nil, d.err
	}
	return &actortransport.DispatchReceipt{
		Stream:   baldaexecution.DefaultCommandStream,
		Sequence: uint64(len(d.commands)),
		Subject:  baldaexecution.SubjectForEnvelope(env),
		MsgID:    actorlayer.DedupeKeyOrID(env),
	}, nil
}

func zulipDeliveryPayloads(t *testing.T, envs []actorlayer.Envelope) []actors.DeliveryPayload {
	t.Helper()
	payloads := make([]actors.DeliveryPayload, 0, len(envs))
	for _, env := range envs {
		if env.To.Target != baldaexecution.ActorTypeDelivery {
			continue
		}
		var payload actors.DeliveryPayload
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			t.Fatalf("decode delivery payload: %v", err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func TestZulipInboundHandler_CommandAccess_HandlesMissingOwnerStore(t *testing.T) {
	dispatcher := &recordingZulipDispatcher{}
	handler := &zulipInboundHandler{
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
	}

	_ = handler.HandleCommand(context.Background(), zulip.InboundCommand{
		Locator:  zulip.NewDMLocator(101),
		SenderID: 101,
		Command:  "locator",
		Direct:   true,
	})

	payloads := zulipDeliveryPayloads(t, dispatcher.commands)
	if len(payloads) != 1 {
		t.Fatalf("delivery payloads = %d, want access denial", len(payloads))
	}
	if payloads[0].Text != "Only the bot owner or collaborators can use this bot." {
		t.Fatalf("reply = %q, want access denial", payloads[0].Text)
	}
}

func TestZulipInboundHandler_Start_DirectMessageOnly(t *testing.T) {
	dispatcher := &recordingZulipDispatcher{}
	handler := &zulipInboundHandler{
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
	}

	_ = handler.HandleCommand(context.Background(), zulip.InboundCommand{
		Locator:  zulip.NewStreamLocator(42, "general"),
		SenderID: 101,
		Command:  "start",
		Direct:   false,
	})

	payloads := zulipDeliveryPayloads(t, dispatcher.commands)
	if len(payloads) != 1 {
		t.Fatalf("delivery payloads = %d, want 1", len(payloads))
	}
	if payloads[0].Text != zulipDirectMessageOnlyText {
		t.Fatalf("reply = %q, want direct message only text", payloads[0].Text)
	}
}

func TestZulipInboundHandler_CommandsRejectExtraArgs(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		args     string
		wantText string
	}{
		{
			name:     "cancel rejects args",
			command:  "cancel",
			args:     "now",
			wantText: zulipCancelUsageText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ownerStore, err := auth.NewOwnerStore(&fakeZulipOwnerKVStore{})
			if err != nil {
				t.Fatalf("NewOwnerStore() error = %v", err)
			}
			if _, err := ownerStore.RegisterOwnerSubject(auth.ZulipSubject(101)); err != nil {
				t.Fatalf("RegisterOwnerSubject() error = %v", err)
			}
			dispatcher := &recordingZulipDispatcher{}
			handler := &zulipInboundHandler{
				ownerStore:      ownerStore,
				actorDispatcher: dispatcher,
				logger:          zerolog.Nop(),
				ownerID:         101,
			}

			_ = handler.HandleCommand(context.Background(), zulip.InboundCommand{
				Locator:  zulip.NewDMLocator(101),
				SenderID: 101,
				Command:  tt.command,
				Args:     tt.args,
				Direct:   true,
			})

			payloads := zulipDeliveryPayloads(t, dispatcher.commands)
			if len(payloads) != 1 {
				t.Fatalf("delivery payloads = %d, want 1", len(payloads))
			}
			if payloads[0].Text != tt.wantText {
				t.Fatalf("reply = %q, want %q", payloads[0].Text, tt.wantText)
			}
		})
	}
}

func TestZulipInboundHandler_Close_DirectMessageOnly(t *testing.T) {
	ownerStore, err := auth.NewOwnerStore(&fakeZulipOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := ownerStore.RegisterOwnerSubject(auth.ZulipSubject(101)); err != nil {
		t.Fatalf("RegisterOwnerSubject() error = %v", err)
	}
	dispatcher := &recordingZulipDispatcher{}
	handler := &zulipInboundHandler{
		ownerStore:      ownerStore,
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
		ownerID:         101,
	}

	_ = handler.HandleCommand(context.Background(), zulip.InboundCommand{
		Locator:  zulip.NewStreamLocator(42, "general"),
		SenderID: 101,
		Command:  "close",
		Direct:   false,
	})

	payloads := zulipDeliveryPayloads(t, dispatcher.commands)
	if len(payloads) != 1 {
		t.Fatalf("delivery payloads = %d, want 1", len(payloads))
	}
	if payloads[0].Text != zulipDirectMessageOnlyText {
		t.Fatalf("reply = %q, want %q", payloads[0].Text, zulipDirectMessageOnlyText)
	}
}

func TestZulipInboundHandler_AutoCommand_TogglesState(t *testing.T) {
	locator := zulip.NewDMLocator(101)
	dispatcher := &recordingZulipDispatcher{}
	handler := &zulipInboundHandler{
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
		autoMaxTurns:    5,
		now:             time.Now,
	}

	handler.handleAutoCommand(context.Background(), locator, "on")

	if len(dispatcher.commands) != 2 {
		t.Fatalf("dispatched commands = %d, want 2", len(dispatcher.commands))
	}
	if dispatcher.commands[0].Namespace != baldaexecution.NamespaceAutoModeCommand {
		t.Fatalf("Namespace = %q, want %q", dispatcher.commands[0].Namespace, baldaexecution.NamespaceAutoModeCommand)
	}
}

func TestZulipInboundHandler_ProcessInbound_IgnoresUnauthorized(t *testing.T) {
	dispatcher := &recordingZulipDispatcher{}
	handler := &zulipInboundHandler{
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
		ownerID:         999, // sender 101 is not owner
	}

	settlement, err := handler.ProcessInbound(context.Background(), zulip.InboundMessage{
		Locator:    zulip.NewDMLocator(101),
		MessageID:  123,
		SenderID:   101,
		Text:       "hello",
		Direct:     true,
		ReceivedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ProcessInbound() error = %v", err)
	}
	if settlement.Outcome != turncmd.InboundTerminal {
		t.Fatalf("settlement.Outcome = %q, want terminal", settlement.Outcome)
	}
	if len(dispatcher.commands) != 0 {
		t.Fatalf("commands dispatched = %d, want 0", len(dispatcher.commands))
	}
}
