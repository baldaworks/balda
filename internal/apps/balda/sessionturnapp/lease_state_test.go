package sessionturnapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const leaseStateSessionID = "tg-1-0"

func TestProviderTurnUsesLeasedStateAndRejectsStaleFormatWrite(t *testing.T) {
	var providerInput string
	providerRunner, providerSessionID := newFormatTestRunner(t, func(invocationID, input string) ([]*adksession.Event, error) {
		providerInput = input
		terminal := adksession.NewEvent(t.Context(), invocationID)
		terminal.TurnComplete = true
		terminal.Content = genai.NewContentFromText("answer", genai.RoleModel)
		return []*adksession.Event{terminal}, nil
	})
	predecessor := &leaseStateRuntime{
		runner: providerRunner, userID: "telegram-user", sessionID: providerSessionID, archiveID: "archive-old",
		values: map[string]any{automode.StateKeyEnabled: false},
	}
	successor := &leaseStateRuntime{archiveID: "archive-new", values: map[string]any{
		stateKeyMessageFormatAnnounced: true,
		stateKeyMessageFormatName:      string(deliveryfmt.NameTelegramRichMarkdown),
		automode.StateKeyEnabled:       true,
	}}
	authority := &leaseStateAuthority{archiveID: predecessor.archiveID, values: predecessor.values}
	service := NewTurnExecutionServiceWithFormats(nil, nil, authority, zerolog.Nop(), automode.DefaultMaxTurns, nil, newFormatTestRegistry(t))
	barrier := newLeaseStateBarrier(NewProviderTurnExecutorFromService(service))
	active := &leaseStateSession{current: predecessor}
	runner := sessionturn.New(leaseStateAccessor{active}, barrier, nil, zerolog.Nop())

	result := make(chan error, 1)
	go func() {
		result <- runner.RunSessionTurnPayload(t.Context(), leaseStatePayload("archive-old", deliveryfmt.DeliveryFormatRichMarkdown))
	}()
	<-barrier.acquired
	active.swap(successor)
	authority.swap(successor.archiveID, successor.values)
	close(barrier.resume)

	err := <-result
	if !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("RunSessionTurnPayload() error = %v, want stale archive conflict", err)
	}
	if !strings.Contains(providerInput, "name: telegram_rich_markdown") {
		t.Fatalf("provider input did not use predecessor format state: %q", providerInput)
	}
	if predecessor.reads.Load() == 0 || authority.reads.Load() != 0 {
		t.Fatalf("lease/current state reads = %d/%d, want lease-only reads", predecessor.reads.Load(), authority.reads.Load())
	}
	if authority.writes.Load() != 1 || authority.lastArchive() != "archive-old" {
		t.Fatalf("write attempts/archive = %d/%q, want 1/archive-old", authority.writes.Load(), authority.lastArchive())
	}
	if predecessor.releases.Load() != 1 {
		t.Fatalf("predecessor releases = %d, want 1", predecessor.releases.Load())
	}
}

func TestProviderTurnUsesLeasedAutoStateAndRejectsStaleWriteBeforeProvider(t *testing.T) {
	var providerCalls atomic.Int32
	providerRunner, providerSessionID := newFormatTestRunner(t, func(invocationID, _ string) ([]*adksession.Event, error) {
		providerCalls.Add(1)
		terminal := adksession.NewEvent(t.Context(), invocationID)
		terminal.TurnComplete = true
		return []*adksession.Event{terminal}, nil
	})
	predecessor := &leaseStateRuntime{
		runner: providerRunner, userID: "telegram-user", sessionID: providerSessionID, archiveID: "archive-old",
		values: map[string]any{
			automode.StateKeyEnabled:  true,
			automode.StateKeyMode:     automode.StateIdle,
			automode.StateKeyMaxTurns: automode.DefaultMaxTurns,
		},
	}
	successor := &leaseStateRuntime{archiveID: "archive-new", values: map[string]any{automode.StateKeyEnabled: false}}
	authority := &leaseStateAuthority{archiveID: predecessor.archiveID, values: predecessor.values}
	service := NewTurnExecutionServiceWithJobEvents(nil, nil, authority, zerolog.Nop(), automode.DefaultMaxTurns)
	barrier := newLeaseStateBarrier(NewProviderTurnExecutorFromService(service))
	active := &leaseStateSession{current: predecessor}
	runner := sessionturn.New(leaseStateAccessor{active}, barrier, nil, zerolog.Nop())

	result := make(chan error, 1)
	go func() {
		result <- runner.RunSessionTurnPayload(t.Context(), leaseStatePayload("archive-old", ""))
	}()
	<-barrier.acquired
	active.swap(successor)
	authority.swap(successor.archiveID, successor.values)
	close(barrier.resume)

	err := <-result
	if !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("RunSessionTurnPayload() error = %v, want stale archive conflict", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0 after rejected auto-state write", providerCalls.Load())
	}
	if predecessor.reads.Load() == 0 || authority.reads.Load() != 0 {
		t.Fatalf("lease/current state reads = %d/%d, want lease-only reads", predecessor.reads.Load(), authority.reads.Load())
	}
	if authority.writes.Load() != 1 || authority.lastArchive() != "archive-old" {
		t.Fatalf("write attempts/archive = %d/%q, want 1/archive-old", authority.writes.Load(), authority.lastArchive())
	}
	if predecessor.releases.Load() != 1 {
		t.Fatalf("predecessor releases = %d, want 1", predecessor.releases.Load())
	}
}

func leaseStatePayload(archiveID string, format deliveryfmt.DeliveryFormat) turncmd.SessionTurnPayload {
	return turncmd.SessionTurnPayload{
		ArchiveID:      archiveID,
		Text:           "hello",
		Source:         turncmd.SourceTelegram,
		Locator:        baldasession.SessionLocator{SessionID: leaseStateSessionID, ChannelType: deliveryfmt.TransportTelegram},
		DeliveryFormat: format,
	}
}

type leaseStateBarrier struct {
	next     sessionturn.Executor
	acquired chan struct{}
	resume   chan struct{}
}

func newLeaseStateBarrier(next sessionturn.Executor) *leaseStateBarrier {
	return &leaseStateBarrier{next: next, acquired: make(chan struct{}), resume: make(chan struct{})}
}

func (e *leaseStateBarrier) ExecuteSessionTurn(ctx context.Context, request sessionturn.Request) error {
	close(e.acquired)
	<-e.resume
	return e.next.ExecuteSessionTurn(ctx, request)
}

type leaseStateSession struct {
	mu      sync.Mutex
	current *leaseStateRuntime
}

func (*leaseStateSession) GetSessionID() string { return leaseStateSessionID }

func (s *leaseStateSession) AcquireRuntime() (sessionturn.RuntimeLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}

func (s *leaseStateSession) swap(next *leaseStateRuntime) {
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
}

type leaseStateAccessor struct{ active *leaseStateSession }

func (a leaseStateAccessor) GetSession(sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a leaseStateAccessor) RestoreSession(context.Context, sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a leaseStateAccessor) EnsureSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return a.active, nil
}
func (a leaseStateAccessor) EnsureTransientSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return a.active, nil
}

type leaseStateRuntime struct {
	runner    *adkrunner.Runner
	userID    string
	sessionID string
	archiveID string
	values    map[string]any
	reads     atomic.Int32
	releases  atomic.Int32
}

func (r *leaseStateRuntime) Runner() *adkrunner.Runner { return r.runner }
func (r *leaseStateRuntime) UserID() string            { return r.userID }
func (r *leaseStateRuntime) SessionID() string         { return r.sessionID }
func (r *leaseStateRuntime) ArchiveID() string         { return r.archiveID }
func (*leaseStateRuntime) RuntimeSnapshotID() string   { return "snapshot" }
func (r *leaseStateRuntime) RuntimeStateValue(_ context.Context, key string) (any, bool, error) {
	r.reads.Add(1)
	value, ok := r.values[key]
	return value, ok, nil
}
func (r *leaseStateRuntime) Release() { r.releases.Add(1) }

type leaseStateAuthority struct {
	mu             sync.Mutex
	archiveID      string
	values         map[string]any
	lastWriteEpoch string
	reads          atomic.Int32
	writes         atomic.Int32
}

func (s *leaseStateAuthority) RuntimeStateValue(_ context.Context, _ baldasession.SessionLocator, key string) (any, bool, error) {
	s.reads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *leaseStateAuthority) UpdateRuntimeStateForArchive(_ context.Context, _ baldasession.SessionLocator, archiveID string, _ map[string]any) (bool, error) {
	s.writes.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastWriteEpoch = archiveID
	return archiveID == s.archiveID, nil
}

func (s *leaseStateAuthority) swap(archiveID string, values map[string]any) {
	s.mu.Lock()
	s.archiveID = archiveID
	s.values = values
	s.mu.Unlock()
}

func (s *leaseStateAuthority) lastArchive() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWriteEpoch
}
