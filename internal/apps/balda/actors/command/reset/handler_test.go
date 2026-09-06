package reset

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type resetSessions struct{ reset, create int }

const resetTestAgent = "balda"

func (*resetSessions) GetSessionInfo(context.Context, string) (session.TopicSessionInfo, error) {
	return session.TopicSessionInfo{AgentName: resetTestAgent, UserID: "tg-1"}, nil
}
func (s *resetSessions) ResetSession(context.Context, session.SessionLocator) error {
	s.reset++
	return nil
}
func (s *resetSessions) CreateSession(context.Context, session.SessionContext, string) error {
	s.create++
	return nil
}
func (*resetSessions) BaldaProviderID() string                       { return resetTestAgent }
func (*resetSessions) GetAgentMetadata(string) session.AgentMetadata { return session.AgentMetadata{} }
func (*resetSessions) TakeStartupNotice(string) string               { return "" }

type resetCanceller struct {
	calls  int
	actor  string
	reason string
}

func (c *resetCanceller) CancelWork(_ context.Context, _ session.SessionLocator, actor, reason string) error {
	c.calls++
	c.actor = actor
	c.reason = reason
	return nil
}

type resetDispatcher struct{ envelopes []actorlayer.Envelope }

func (d *resetDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandlerResetsAndRecreatesSession(t *testing.T) {
	sessions, canceller, dispatcher := &resetSessions{}, &resetCanceller{}, &resetDispatcher{}
	h := New(sessions, canceller, dispatcher)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{}`, SessionID: "tg-1-0"}
	payload := commandcmd.Payload{Version: commandcmd.SchemaVersion, Name: "reset", Locator: locator, Transport: "telegram", Principal: "tg-1", Access: commandcmd.Access{SessionCommands: true}, Conversation: commandcmd.Conversation{Direct: true}, Invocation: commandcmd.Invocation{Root: "/"}}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
		t.Fatal(err)
	}
	if sessions.reset != 1 || sessions.create != 1 || canceller.calls != 1 {
		t.Fatalf("calls reset=%d create=%d cancel=%d", sessions.reset, sessions.create, canceller.calls)
	}
	if canceller.actor != "command.reset" || canceller.reason != "session canceled by reset command" {
		t.Fatalf("cancel metadata actor=%q reason=%q", canceller.actor, canceller.reason)
	}
	if len(dispatcher.envelopes) != 1 || dispatcher.envelopes[0].DedupeKey != "op-1:delivery:reset-welcome" {
		t.Fatalf("delivery = %+v", dispatcher.envelopes)
	}
}
