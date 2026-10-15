package chatfx

import (
	"errors"
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
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "saved-id", UserID: "saved-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Activated || first.SessionLabel != "auto" || first.SessionID != "saved-id" {
		t.Fatalf("initial preparation = %+v", first)
	}
	prepared, err := NewIngressSessionAdapter(manager).Prepare(t.Context(), ingressapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "changed-inbound-id", UserID: "requester", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Ready || prepared.SessionID != first.SessionID || prepared.UserID != "saved-user" || prepared.RequesterUserID != "requester" || prepared.SessionLabel != "auto" {
		t.Fatalf("saved preparation = %+v", prepared)
	}
	if prepared.AgentSessionID != first.AgentSessionID || prepared.RuntimeSnapshotID != first.RuntimeSnapshotID || prepared.Model != first.Model {
		t.Fatalf("shared preparation identity changed: first=%+v, second=%+v", first, prepared)
	}
}

func TestIngressPreparationMainDMLabelIsCompositionDefault(t *testing.T) {
	prepared, err := NewIngressSessionAdapter(newTestSessionManager(t)).Prepare(t.Context(), ingressapp.InboundContext{
		ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`, SessionID: "new-dm", UserID: "user", Direct: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SessionLabel != "balda" {
		t.Fatalf("main DM label = %q", prepared.SessionLabel)
	}
}
