package session

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/session"
)

func TestTopicSessionGetSessionIDReturnsTransportSessionID(t *testing.T) {
	ts := &TopicSession{
		sessionID: "tg-10001-0",
		userID:    "tg-10001",
	}

	if got := ts.GetSessionID(); got != "tg-10001-0" {
		t.Fatalf("GetSessionID() = %q, want tg-10001-0", got)
	}
	if got := ts.GetUserID(); got != "tg-10001" {
		t.Fatalf("GetUserID() = %q, want tg-10001", got)
	}
}

func TestTopicSessionRuntimeStateValueReadsCurrentSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessionSvc := session.InMemoryService()
	created, err := sessionSvc.Create(ctx, &session.CreateRequest{
		AppName:   "balda-test",
		UserID:    "tg-10001",
		SessionID: "agent-session-1",
		State: map[string]any{
			"balda_memory_version": "1",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	current, err := sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   "balda-test",
		UserID:    "tg-10001",
		SessionID: "agent-session-1",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	event := session.NewEvent(context.Background(), "invocation-1")
	event.Actions.StateDelta = map[string]any{"balda_memory_version": "2"}
	if err := sessionSvc.AppendEvent(ctx, current.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	ts := &TopicSession{
		sessionID: "transport-session-1",
	}
	withTestRuntime(ts, sessionSvc, created.Session, nil)
	got, ok, err := ts.RuntimeStateValue(ctx, "balda_memory_version")
	if err != nil {
		t.Fatalf("RuntimeStateValue() error = %v", err)
	}
	if !ok {
		t.Fatal("RuntimeStateValue() ok = false, want true")
	}
	if got != "2" {
		t.Fatalf("RuntimeStateValue() = %v, want 2", got)
	}
}
