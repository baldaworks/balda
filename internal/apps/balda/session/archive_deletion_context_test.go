package session

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type outcomeArchive struct {
	ArchiveLifecycle
	complete func(context.Context, string, string) error
}

func (a outcomeArchive) Complete(ctx context.Context, token, outcome string) error {
	return a.complete(ctx, token, outcome)
}

type outcomeService struct {
	adksession.Service
	get func(context.Context, *adksession.GetRequest) (*adksession.GetResponse, error)
}

func (s outcomeService) Get(ctx context.Context, r *adksession.GetRequest) (*adksession.GetResponse, error) {
	return s.get(ctx, r)
}

func TestArchiveDeletionReconcilesExpiredContext(t *testing.T) {
	for _, completionErr := range []error{nil, chatarchivecmd.ErrUnavailable} {
		t.Run(map[bool]string{false: "completed", true: "completion unavailable"}[completionErr != nil], func(t *testing.T) {
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			defer cancel()
			var readCtx context.Context
			m := &Manager{archive: outcomeArchive{complete: func(ctx context.Context, _, outcome string) error {
				if ctx.Err() != nil {
					t.Errorf("completion context expired: %v", ctx.Err())
				}
				if ctx != readCtx {
					t.Error("read and completion do not share one budget")
				}
				if outcome != chatarchivecmd.OutcomeDestroyed {
					t.Errorf("outcome=%s", outcome)
				}
				return completionErr
			}}}
			svc := outcomeService{get: func(ctx context.Context, _ *adksession.GetRequest) (*adksession.GetResponse, error) {
				readCtx = ctx
				if ctx.Err() != nil {
					t.Errorf("outcome read context expired: %v", ctx.Err())
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > cleanupTimeout {
					t.Error("outcome read lacks finite cleanup budget")
				}
				return nil, adksession.ErrNotFound
			}}
			destroyed, err := m.completeArchiveDeletion(ctx, &chatarchivecmd.Intent{}, svc, "app", ensureTestAuthor, context.DeadlineExceeded)
			if !destroyed || !errors.Is(err, completionErr) {
				t.Fatalf("destroyed=%v, error=%v; want true, %v", destroyed, err, completionErr)
			}
		})
	}
}

func TestArchiveDeletionOutcomeReadIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		m := &Manager{archive: outcomeArchive{complete: func(ctx context.Context, _, outcome string) error {
			if outcome != chatarchivecmd.StatusUncertain {
				t.Errorf("outcome=%s", outcome)
			}
			return ctx.Err()
		}}}
		svc := outcomeService{get: func(ctx context.Context, _ *adksession.GetRequest) (*adksession.GetResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		destroyed, err := m.completeArchiveDeletion(t.Context(), &chatarchivecmd.Intent{}, svc, "app", ensureTestAuthor, context.DeadlineExceeded)
		if destroyed || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("destroyed=%v, error=%v", destroyed, err)
		}
		if elapsed := time.Since(start); elapsed != cleanupTimeout {
			t.Fatalf("outcome budget=%v, want %v", elapsed, cleanupTimeout)
		}
	})
}

type committedDeadlineService struct {
	adksession.Service
	afterDelete func()
}

func (s committedDeadlineService) Delete(ctx context.Context, r *adksession.DeleteRequest) error {
	if err := s.Service.Delete(ctx, r); err != nil {
		return err
	}
	if s.afterDelete != nil {
		s.afterDelete()
	}
	return context.DeadlineExceeded
}

func TestArchiveResetReconcilesCommittedDeadline(t *testing.T) {
	const callerCanceled = "caller canceled"
	const managerStopping = "manager stopping"
	for _, mode := range []string{"active", "persisted", callerCanceled, managerStopping} {
		t.Run(mode, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = `{"root_provider_id":"old"}`
			m := f.manager()
			first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			event := adksession.NewEvent(t.Context(), "old-turn")
			event.Author = ensureTestAuthor
			event.Actions.StateDelta = map[string]any{"old-conversation": "old-state"}
			if err := testRuntimeService(first).AppendEvent(t.Context(), testRuntimeSession(first), event); err != nil {
				t.Fatal(err)
			}
			if _, err := f.provider.ChatArchive().Record(t.Context(), chatarchivecmd.Message{SessionID: first.GetArchiveID(), Source: "original", Role: ensureTestAuthor, Text: "retain me", ObservedAt: m.currentTime()}); err != nil {
				t.Fatal(err)
			}
			f.runtime.ProviderBindingJSON = `{"root_provider_id":"current"}`
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			svc := committedDeadlineService{Service: testRuntimeService(first), afterDelete: func() {
				if mode == callerCanceled {
					cancel()
				}
				if mode == managerStopping {
					m.stopping.Store(true)
				}
			}}
			first.runtime.value.runtime.SessionSvc = svc
			if mode == "persisted" {
				m = f.manager()
				f.runtime.SessionSvc = svc
			}
			err = m.ResetSession(ctx, archiveInput().Locator)
			switch mode {
			case callerCanceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("reset=%v, want cancellation", err)
				}
			case managerStopping:
				if !errors.Is(err, errManagerStopping) {
					t.Fatalf("reset=%v, want stopping", err)
				}
			default:
				if err != nil {
					t.Fatalf("reset=%v", err)
				}
			}
			old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
			if err != nil || old.Status != chatarchivecmd.StatusEnded {
				t.Fatalf("old archive=%#v, %v", old, err)
			}
			messages, err := f.provider.ChatArchive().Messages(t.Context(), first.GetArchiveID(), 0, 100)
			if err != nil || len(messages) != 1 || messages[0].Text != "retain me" {
				t.Fatalf("messages=%#v, %v", messages, err)
			}
			next, err := m.EnsureSession(ctx, archiveInput(), "saved")
			if mode == callerCanceled || mode == managerStopping {
				if err == nil || f.builder.freshCalls != 1 {
					t.Fatalf("replacement exposed: %p, %v; fresh calls=%d", next, err, f.builder.freshCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if next.GetArchiveID() == first.GetArchiveID() || next.GetAgentSessionID() != first.GetAgentSessionID() || f.builder.freshCalls != 2 {
				t.Fatal("reset did not create exactly one fresh lifetime at same routing identity")
			}
			// Fresh archive activation may record its own epoch event.
			for i := 0; i < testRuntimeSession(next).Events().Len(); i++ {
				if testRuntimeSession(next).Events().At(i).ID == event.ID {
					t.Fatal("fresh session retained predecessor event")
				}
			}
			if _, err := testRuntimeSession(next).State().Get("old-conversation"); err == nil {
				t.Fatal("fresh session retained predecessor state")
			}
			if next.runtime.value.runtime.ProviderBindingJSON != f.runtime.ProviderBindingJSON || next.runtime.value.runtime.ProviderBindingJSON == first.runtime.value.runtime.ProviderBindingJSON {
				t.Fatalf("fresh provider binding=%s, old=%s", next.runtime.value.runtime.ProviderBindingJSON, first.runtime.value.runtime.ProviderBindingJSON)
			}
		})
	}
}

type expiredDeleteService struct{ adksession.Service }

func (s expiredDeleteService) Delete(ctx context.Context, r *adksession.DeleteRequest) error {
	if err := s.Service.Delete(ctx, r); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

type resetOutcomeArchive struct {
	outcomeArchive
	session chatarchivecmd.Session
}

func (a resetOutcomeArchive) Get(context.Context, string) (chatarchivecmd.Session, error) {
	return a.session, nil
}
func (a resetOutcomeArchive) Pending(context.Context, string, string) (chatarchivecmd.Intent, error) {
	return chatarchivecmd.Intent{}, chatarchivecmd.ErrNotFound
}
func (a resetOutcomeArchive) Begin(_ context.Context, intent chatarchivecmd.Intent) (chatarchivecmd.Intent, error) {
	return intent, nil
}

type endContextHooks struct{ check func(context.Context) }

func (h endContextHooks) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if event.Name == hookcmd.SessionEnd {
		h.check(ctx)
	}
	return hookcmd.Result{}, nil
}

func TestArchiveResetExpiredDeletionCompletesCleanup(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		svc := expiredDeleteService{Service: adksession.InMemoryService()}
		loc := testTelegramLocator(10, 0)
		created, err := svc.Create(t.Context(), &adksession.CreateRequest{AppName: baldaRuntimeAppName, UserID: ensureTestAuthor, SessionID: loc.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		ts := &TopicSession{sessionID: loc.SessionID, userID: ensureTestAuthor, locator: loc, hooksStarted: true, workspaceDir: dir}
		completions, ends, closes := 0, 0, 0
		ts.runtime = newRuntimeSlot(runtimeSlotConfig{runtime: &BuiltRuntime{SessionSvc: svc, Close: func() error { closes++; return nil }}, session: created.Session, userID: ensureTestAuthor, sessionID: loc.SessionID, archiveID: "old"})
		m := &Manager{logger: zerolog.Nop(), sessions: map[string]*TopicSession{loc.SessionID: ts}, archive: resetOutcomeArchive{
			session: chatarchivecmd.Session{ID: "old", RuntimeID: loc.SessionID},
			outcomeArchive: outcomeArchive{complete: func(ctx context.Context, _, outcome string) error {
				completions++
				if ctx.Err() != nil || outcome != chatarchivecmd.OutcomeDestroyed {
					t.Errorf("completion=%s, context=%v", outcome, ctx.Err())
				}
				return ctx.Err()
			}},
		}, hooks: endContextHooks{check: func(ctx context.Context) {
			ends++
			if ctx.Err() != nil {
				t.Errorf("end hook context=%v", ctx.Err())
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > cleanupTimeout {
				t.Error("end hook lacks bounded cleanup context")
			}
		}}}
		start := time.Now()
		if err := m.ResetSession(t.Context(), loc); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != cleanupTimeout {
			t.Fatalf("delete duration=%v, want %v", elapsed, cleanupTimeout)
		}
		if !ts.runtimeDeleted || !ts.runtimeClosed || completions != 1 || ends != 1 || closes != 1 || len(m.sessions) != 0 {
			t.Fatalf("cleanup: deleted=%v closed=%v completions=%d ends=%d closes=%d active=%d", ts.runtimeDeleted, ts.runtimeClosed, completions, ends, closes, len(m.sessions))
		}
	})
}

func TestArchiveShutdownReconcilesCommittedDeadline(t *testing.T) {
	f := newArchiveFixture(t)
	m := f.manager()
	first, err := m.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	first.runtime.value.runtime.SessionSvc = committedDeadlineService{Service: testRuntimeService(first)}
	m.sessionsPersistent = false
	m.stopping.Store(true)
	m.stopAllWithContext(t.Context())
	old, err := f.provider.ChatArchive().Get(t.Context(), first.GetArchiveID())
	if err != nil || old.Status != chatarchivecmd.StatusEnded || old.EndReason != "shutdown" {
		t.Fatalf("shutdown archive=%#v, %v", old, err)
	}
	if _, err := f.provider.ChatArchive().Pending(t.Context(), "telegram", "10:0"); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("shutdown destruction not settled: %v", err)
	}
	if !first.runtimeDeleted || !first.runtimeClosed || f.builder.freshCalls != 1 {
		t.Fatalf("shutdown deleted=%v closed=%v fresh calls=%d", first.runtimeDeleted, first.runtimeClosed, f.builder.freshCalls)
	}
}
