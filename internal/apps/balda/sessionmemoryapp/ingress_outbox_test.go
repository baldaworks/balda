package sessionmemoryapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
	"github.com/rs/zerolog"
)

func TestIngressOutboxPublisherPersistsBeforeTransientPublishFailure(t *testing.T) {
	t.Parallel()
	store := &fakeIngressOutboxStore{}
	downstream := &fakeExportPublisher{err: errors.New("nats unavailable")}
	publisher, err := NewIngressOutboxPublisher(store, downstream, IngressOutboxConfig{Enabled: true, WorkerID: "test-worker"}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewIngressOutboxPublisher() error = %v", err)
	}
	publisher.now = func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) }
	if err := publisher.Publish(context.Background(), testIngressExport(t)); err != nil {
		t.Fatalf("Publish() error = %v, want durable enqueue success", err)
	}
	if got := store.enqueueCount(); got != 1 {
		t.Fatalf("enqueue count = %d, want 1", got)
	}
	if got := store.releaseCount(); got != 1 {
		t.Fatalf("release count = %d, want 1", got)
	}
	if got := store.publishedCount(); got != 0 {
		t.Fatalf("published count = %d, want 0", got)
	}
	if got := store.lastRetryAt(); !got.Equal(time.Date(2026, 8, 5, 12, 0, 0, int((250 * time.Millisecond).Nanoseconds()), time.UTC)) {
		t.Fatalf("retry time = %v, want exponential base delay", got)
	}
}

func TestIngressOutboxPublisherSettlesOnlyAfterPubAck(t *testing.T) {
	t.Parallel()
	store := &fakeIngressOutboxStore{}
	export := testIngressExport(t)
	record, err := sessionmemorycmd.NewIngressRecord(export, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	store.records = []sessionmemorycmd.IngressRecord{record}
	publisher, err := NewIngressOutboxPublisher(store, &fakeExportPublisher{}, IngressOutboxConfig{Enabled: true, WorkerID: "test-worker"}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewIngressOutboxPublisher() error = %v", err)
	}
	publisher.now = func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) }
	if err := publisher.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if got := store.publishedCount(); got != 1 {
		t.Fatalf("published count = %d, want 1", got)
	}
	if got := store.releaseCount(); got != 0 {
		t.Fatalf("release count = %d, want 0", got)
	}
}

func TestIngressOutboxPublisherTerminalsAfterBoundedAttempts(t *testing.T) {
	t.Parallel()
	record, err := sessionmemorycmd.NewIngressRecord(testIngressExport(t), time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	record.Attempts = 2
	store := &fakeIngressOutboxStore{records: []sessionmemorycmd.IngressRecord{record}}
	publisher, err := NewIngressOutboxPublisher(store, &fakeExportPublisher{err: errors.New("unavailable")}, IngressOutboxConfig{Enabled: true, WorkerID: "test-worker", MaxAttempts: 2}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewIngressOutboxPublisher() error = %v", err)
	}
	if err := publisher.Flush(context.Background()); err == nil {
		t.Fatal("Flush() error = nil, want downstream failure")
	}
	if !store.lastTerminal() {
		t.Fatal("terminal release = false, want true after maximum attempts")
	}
}

func testIngressExport(t *testing.T) sessionmemorycmd.Export {
	t.Helper()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:chat-1", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"},
		"turn-1",
		time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		"hello",
		"world",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn() export error = %v", err)
	}
	return export
}

type fakeExportPublisher struct{ err error }

func (p *fakeExportPublisher) Publish(context.Context, sessionmemorycmd.Export) error { return p.err }

type fakeIngressOutboxStore struct {
	mu        sync.Mutex
	records   []sessionmemorycmd.IngressRecord
	enqueues  int
	published int
	released  int
	retryAt   *time.Time
	terminal  bool
}

func (s *fakeIngressOutboxStore) EnqueueSessionMemoryIngress(_ context.Context, record sessionmemorycmd.IngressRecord) (sessionmemorycmd.IngressRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueues++
	record.ScopeSequence = uint64(len(s.records) + 1)
	s.records = append(s.records, record)
	return record, true, nil
}

func (s *fakeIngressOutboxStore) ClaimSessionMemoryIngress(_ context.Context, _ string, _ time.Time, _ time.Time, _ int) ([]sessionmemorycmd.IngressRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sessionmemorycmd.IngressRecord(nil), s.records...), nil
}

func (s *fakeIngressOutboxStore) MarkSessionMemoryIngressPublished(_ context.Context, _ string, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published++
	return nil
}

func (s *fakeIngressOutboxStore) ReleaseSessionMemoryIngress(_ context.Context, _ string, _ string, _ string, terminal bool, retryAt *time.Time, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released++
	s.terminal = terminal
	if retryAt != nil {
		value := *retryAt
		s.retryAt = &value
	}
	return nil
}

func (s *fakeIngressOutboxStore) ReplaySessionMemoryIngress(context.Context, string, string, string, time.Time) error {
	return nil
}

func (s *fakeIngressOutboxStore) SessionMemoryIngressStats(context.Context, time.Time) (sessionmemorycmd.IngressOutboxStats, error) {
	return sessionmemorycmd.IngressOutboxStats{}, nil
}

func (s *fakeIngressOutboxStore) enqueueCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueues
}
func (s *fakeIngressOutboxStore) publishedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}
func (s *fakeIngressOutboxStore) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

func (s *fakeIngressOutboxStore) lastRetryAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryAt == nil {
		return time.Time{}
	}
	return *s.retryAt
}

func (s *fakeIngressOutboxStore) lastTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}
