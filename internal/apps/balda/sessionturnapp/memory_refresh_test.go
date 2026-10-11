package sessionturnapp

import (
	"context"
	"errors"
	"iter"
	"strconv"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

func TestGlobalFactChangesRefreshActiveAndRestoredSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	kv := &refreshMemoryKV{values: make(map[string]any)}
	store := memory.NewStore(kv, "", true)
	management := memory.NewManagement(store, kv)
	fact, err := management.Create(ctx, memorycmd.Create{Fact: "first current fact"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := adksession.InMemoryService()
	agent, err := adkagent.New(adkagent.Config{Name: "memory-refresh-test", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			event := adksession.NewEvent(ctx, ctx.InvocationID())
			event.TurnComplete = true
			yield(event, nil)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	adkRunner, err := adkrunner.New(adkrunner.Config{AppName: "memory-refresh-test", Agent: agent, SessionService: sessions})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(ctx, &adksession.CreateRequest{AppName: "memory-refresh-test", UserID: "telegram-user"})
	if err != nil {
		t.Fatal(err)
	}
	active := &refreshActiveSession{runner: adkRunner, sessions: sessions, id: created.Session.ID()}
	accessor := &refreshSessionAccessor{session: active}
	execution := NewTurnExecutionServiceWithJobEvents(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
	queued := sessionturn.New(accessor, NewProviderTurnExecutorFromService(execution), refreshMemoryReader{store}, zerolog.Nop())
	var priorHistory int
	run := func(want string, refresh bool) {
		t.Helper()
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Content != want {
			t.Fatalf("owner snapshot=%q,want%q", snapshot.Content, want)
		}
		payload := turncmd.SessionTurnPayload{Text: "continue the same conversation", UserID: "telegram-user", Locator: deliverycmd.Locator{SessionID: "tg-1-0", ChannelType: "telegram"}}
		if err := queued.RunSessionTurnPayload(ctx, payload); err != nil {
			t.Fatal(err)
		}
		current, err := active.runtimeSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if current.ID() != created.Session.ID() {
			t.Fatal("memory refresh changed session identity")
		}
		for key, wantValue := range map[string]string{memory.MemoryStateKey: want, memory.MemoryVersionStateKey: strconv.FormatInt(snapshot.Version, 10), memory.MemoryUpdatedAtStateKey: snapshot.UpdatedAt} {
			value, err := current.State().Get(key)
			if err != nil || value != wantValue {
				t.Fatalf("state %s=%v,error=%v,want%q", key, value, err, wantValue)
			}
		}
		events := current.Events()
		if events.Len() <= priorHistory {
			t.Fatal("refresh erased or failed to extend history")
		}
		var lastInput string
		for event := range events.All() {
			if event.Content != nil && event.Content.Role == "user" {
				lastInput = visibleFormatTestText(event.Content)
			}
		}
		if strings.Contains(lastInput, "[application-memory]") != refresh {
			t.Fatalf("refresh=%t,lastInput=%q", refresh, lastInput)
		}
		if refresh && want == "" && !strings.Contains(lastInput, "There are no current application facts.") {
			t.Fatal("empty current boundary missing")
		}
		priorHistory = events.Len()
	}
	run("first current fact", true)
	editor, err := management.Get(ctx, fact.ID, memorycmd.Authority{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Edit(ctx, memorycmd.Edit{ID: fact.ID, Fact: "edited current fact", ExpectedVersion: editor.ExpectedVersion}); err != nil {
		t.Fatal(err)
	}
	run("edited current fact", true)
	// The queued restore path reads the persisted cursor without resetting history.
	accessor.restore = true
	editor, err = management.Get(ctx, fact.ID, memorycmd.Authority{})
	if err != nil {
		t.Fatal(err)
	}
	if err := management.Delete(ctx, memorycmd.Delete{ID: fact.ID, ExpectedVersion: editor.ExpectedVersion}); err != nil {
		t.Fatal(err)
	}
	run("", true)
	run("", false)
	if accessor.restores != 1 {
		t.Fatalf("restore calls=%d,want1", accessor.restores)
	}
	current, err := active.runtimeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var historicalFact bool
	for event := range current.Events().All() {
		if event.Content != nil && strings.Contains(visibleFormatTestText(event.Content), "first current fact") {
			historicalFact = true
		}
	}
	if !historicalFact {
		t.Fatal("memory deletion erased historical conversation")
	}
}

type refreshMemoryKV struct{ values map[string]any }

func (s *refreshMemoryKV) GetJSON(_ context.Context, key string) (any, bool, error) {
	value, ok := s.values[key]
	return value, ok, nil
}
func (s *refreshMemoryKV) SetJSON(_ context.Context, key string, value any) error {
	s.values[key] = value
	return nil
}
func (*refreshMemoryKV) CheckAuthority(context.Context, memorycmd.Authority) error { return nil }
func (s *refreshMemoryKV) Commit(ctx context.Context, m memorycmd.Mutation) error {
	return s.SetJSON(ctx, memorycmd.Key, m.Record)
}

type refreshMemoryReader struct{ store *memory.Store }

func (r refreshMemoryReader) Enabled() bool { return r.store.MemoryEnabled() }
func (r refreshMemoryReader) Snapshot(ctx context.Context) (sessionturn.MemorySnapshot, error) {
	s, err := r.store.Snapshot(ctx)
	return sessionturn.MemorySnapshot{Content: s.Content, Version: s.Version, UpdatedAt: s.UpdatedAt}, err
}

type refreshActiveSession struct {
	runner   *adkrunner.Runner
	sessions adksession.Service
	id       string
}

func (s *refreshActiveSession) GetRunner() *adkrunner.Runner { return s.runner }
func (*refreshActiveSession) GetRuntimeSnapshotID() string   { return "" }
func (*refreshActiveSession) GetSessionID() string           { return "tg-1-0" }
func (s *refreshActiveSession) GetAgentSessionID() string    { return s.id }
func (*refreshActiveSession) GetUserID() string              { return "telegram-user" }
func (s *refreshActiveSession) runtimeSession(ctx context.Context) (adksession.Session, error) {
	r, err := s.sessions.Get(ctx, &adksession.GetRequest{AppName: "memory-refresh-test", UserID: "telegram-user", SessionID: s.id})
	if err != nil {
		return nil, err
	}
	return r.Session, nil
}
func (s *refreshActiveSession) RuntimeStateValue(ctx context.Context, key string) (any, bool, error) {
	session, err := s.runtimeSession(ctx)
	if err != nil {
		return nil, false, err
	}
	value, err := session.State().Get(key)
	if errors.Is(err, adksession.ErrStateKeyNotExist) {
		return nil, false, nil
	}
	return value, err == nil, err
}

type refreshSessionAccessor struct {
	session  sessionturn.ActiveSession
	restore  bool
	restores int
}

func (a *refreshSessionAccessor) GetSession(sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	if a.restore {
		return nil, errors.New("session requires lazy restore")
	}
	return a.session, nil
}
func (a *refreshSessionAccessor) RestoreSession(context.Context, sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	a.restore = false
	a.restores++
	return a.session, nil
}
func (*refreshSessionAccessor) EnsureSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return nil, errors.New("unexpected session creation")
}
func (*refreshSessionAccessor) EnsureTransientSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return nil, errors.New("unexpected transient session")
}
