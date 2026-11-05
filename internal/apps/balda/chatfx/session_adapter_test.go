package chatfx

import (
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

func TestSessionAdapterPrepareDependencyFailureIsRetryable(t *testing.T) {
	storeErr := errors.New("storage unavailable")
	manager, err := baldasession.NewManager(baldasession.ManagerParams{AgentBuilder: &fakeAgentBuilder{}, RuntimeManager: &fakeRuntimeManager{}, SessionStore: &fakeSessionStore{getErr: storeErr}, Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
	// An unavailable manager dependency must preserve retryable admission instead
	// of turning a temporarily unavailable session into terminal rejection.
	_, err = NewSessionAdapter(manager).Prepare(t.Context(), chatapp.InboundContext{
		SessionID: "new", ChannelType: "telegram", AddressKey: "new", UserID: "user",
	})
	if err == nil {
		t.Fatal("expected preparation failure")
	}
	if !actorlayer.IsRetryableError(err) {
		t.Fatalf("error = %v, want retryable preparation failure", err)
	}
	if !errors.Is(err, storeErr) {
		t.Fatal("storage error lost")
	}
}

func TestConversationalPreparationPortsUseSavedSessionIdentity(t *testing.T) {
	manager := newTestSessionManager(t)
	first, err := NewSessionAdapter(manager).Prepare(t.Context(), chatapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "saved-id", UserID: "saved-user", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Activation == nil || first.SessionID != "saved-id" {
		t.Fatalf("initial preparation = %+v", first)
	}
	if first.Activation == nil || first.Activation.Kind != "created" || first.Activation.SessionID != first.SessionID {
		t.Fatalf("initial activation = %+v", first.Activation)
	}
	ingressAdapter := NewIngressSessionAdapter(manager)
	prepared, err := ingressAdapter.Prepare(t.Context(), ingressapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "changed-inbound-id", UserID: "requester", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Ready || prepared.SessionID != first.SessionID || prepared.UserID != "saved-user" || prepared.RequesterUserID != "requester" {
		t.Fatalf("saved preparation = %+v", prepared)
	}
	if prepared.AgentSessionID != first.AgentSessionID || prepared.RuntimeSnapshotID != first.RuntimeSnapshotID {
		t.Fatalf("shared preparation identity changed: first=%+v, second=%+v", first, prepared)
	}
	if prepared.Activation == nil || !reflect.DeepEqual(prepared.Activation, first.Activation) || prepared.Activation == first.Activation {
		t.Fatalf("retry activation = %+v, first = %+v", prepared.Activation, first.Activation)
	}
	prepared.Activation.Runtime.MCPServers = append(prepared.Activation.Runtime.MCPServers, "mutated")
	if len(first.Activation.Runtime.MCPServers) == len(prepared.Activation.Runtime.MCPServers) {
		t.Fatalf("activation snapshots alias MCP slices: first=%+v second=%+v", first.Activation, prepared.Activation)
	}
	acknowledger, ok := ingressAdapter.(ingressapp.ActivationAcknowledger)
	if !ok || !acknowledger.AcknowledgeActivation(first.SessionID, *first.Activation) {
		t.Fatal("expected ingress adapter to acknowledge pending activation")
	}
	reused, err := ingressAdapter.Prepare(t.Context(), ingressapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "saved-id", UserID: "requester", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused.Activation != nil {
		t.Fatalf("active reuse carried lifecycle outcome: %+v", reused)
	}
}

func TestIngressPreparationMainDMLabelIsCompositionDefault(t *testing.T) {
	manager := newTestSessionManager(t)
	prepared, err := NewIngressSessionAdapter(manager).Prepare(t.Context(), ingressapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "new-dm", UserID: "user", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := manager.GetSession(baldasession.SessionLocator{SessionID: prepared.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := topic.GetKind(); got != baldasession.KindDM {
		t.Fatalf("session kind = %q, want %q", got, baldasession.KindDM)
	}
	if got := topic.GetAgentName(); got != "balda" {
		t.Fatalf("main DM label = %q, want balda", got)
	}
}

func TestSessionAdapterClassifiesSharedConversation(t *testing.T) {
	manager := newTestSessionManager(t)
	prepared, err := NewSessionAdapter(manager).Prepare(t.Context(), chatapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9002:0", AddressJSON: `{"chat_id":9002,"topic_id":0}`,
		SessionID: "group-chat", UserID: "user", Direct: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := manager.GetSession(baldasession.SessionLocator{SessionID: prepared.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := topic.GetKind(); got != baldasession.KindChat {
		t.Fatalf("session kind = %q, want %q", got, baldasession.KindChat)
	}
}
