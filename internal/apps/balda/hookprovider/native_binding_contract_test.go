package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
)

func TestNativeBindingContract(t *testing.T) {
	hooknativetest.RunBindingContract(t, newCurrentBindingFixture)
}

func newCurrentBindingFixture(t testing.TB, runner hooknativetest.Runner) hooknativetest.BindingFixture {
	t.Helper()
	service, err := New(runnerFunc(runner))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	trusted := hookcmd.Event{SessionID: "trusted-session", Provider: "opaque-provider", CWD: t.TempDir(), Source: "native"}
	binding, err := service.Bind(t.Context(), BindingScope{SessionID: trusted.SessionID, Provider: trusted.Provider, CWD: trusted.CWD})
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadBinding(binding.Reference())
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	return hooknativetest.BindingFixture{Binding: &currentBindingDriver{binding: read, release: binding.Release}, Trusted: trusted}
}

type currentBindingDriver struct {
	binding Binding
	release func() error
}

func (d *currentBindingDriver) Invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return d.binding.Invoke(ctx, event)
}

func (d *currentBindingDriver) InvokeUnauthenticated(ctx context.Context, event hookcmd.Event) error {
	untrusted := d.binding
	untrusted.Token = strings.Repeat("0", len(untrusted.Token))
	_, err := untrusted.Invoke(ctx, event)
	return err
}

func (d *currentBindingDriver) InvokeWithOrigin(ctx context.Context, event hookcmd.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.binding.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+d.binding.Token)
	request.Header.Set("Origin", "https://untrusted.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		return errors.New("cross-origin native hook request accepted")
	}
	return hookcmd.ErrUnavailable
}

func (d *currentBindingDriver) SignalReady(ctx context.Context) error {
	return d.binding.SignalReady(ctx)
}

func (d *currentBindingDriver) WaitReady(ctx context.Context) error {
	return d.binding.WaitReady(ctx)
}

func (d *currentBindingDriver) Release() error { return d.release() }

type runnerFunc func(context.Context, hookcmd.Event) (hookcmd.Result, error)

func (f runnerFunc) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return f(ctx, event)
}

type releasingRunner struct {
	mu       sync.Mutex
	released []string
}

func (*releasingRunner) Run(context.Context, hookcmd.Event) (hookcmd.Result, error) {
	return hookcmd.Result{}, nil
}

func (r *releasingRunner) ReleaseSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, sessionID)
}

func TestBindingReleasesDeduplicationAfterLastSessionBinding(t *testing.T) {
	runner := &releasingRunner{}
	service, err := New(runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	scope := BindingScope{SessionID: "session", Provider: "provider", CWD: t.TempDir()}
	first, err := service.Bind(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Bind(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got := len(runner.released)
	runner.mu.Unlock()
	if got != 0 {
		t.Fatalf("releases after first binding = %d, want 0", got)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.released) != 1 || runner.released[0] != scope.SessionID {
		t.Fatalf("released sessions = %v, want [%q]", runner.released, scope.SessionID)
	}
}

type blockingReleaseRunner struct {
	started chan struct{}
	unblock chan struct{}
	calls   atomic.Int64
}

func (*blockingReleaseRunner) Run(context.Context, hookcmd.Event) (hookcmd.Result, error) {
	return hookcmd.Result{}, nil
}

func (r *blockingReleaseRunner) ReleaseSession(string) {
	if r.calls.Add(1) == 1 {
		close(r.started)
		<-r.unblock
	}
}

func TestBindingReleaseCannotClearReboundSessionGeneration(t *testing.T) {
	runner := &blockingReleaseRunner{started: make(chan struct{}), unblock: make(chan struct{})}
	service, err := New(runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	scope := BindingScope{SessionID: "session", Provider: "provider", CWD: t.TempDir()}
	oldBinding, err := service.Bind(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	oldGeneration := service.sessionGenerations[scope.SessionID].id
	service.mu.Unlock()
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- oldBinding.Release() }()
	<-runner.started

	type bindResult struct {
		binding *Binding
		err     error
	}
	rebound := make(chan bindResult, 1)
	go func() {
		binding, bindErr := service.Bind(t.Context(), scope)
		rebound <- bindResult{binding: binding, err: bindErr}
	}()
	select {
	case result := <-rebound:
		close(runner.unblock)
		<-releaseDone
		if result.binding != nil {
			_ = result.binding.Release()
		}
		t.Fatalf("Bind() completed before prior generation cleanup: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}

	close(runner.unblock)
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	result := <-rebound
	if result.err != nil {
		t.Fatal(result.err)
	}
	service.mu.Lock()
	generation := service.sessionGenerations[scope.SessionID]
	service.mu.Unlock()
	if generation.id <= oldGeneration || generation.refs != 1 {
		t.Fatalf("rebound generation = %+v, old generation = %d", generation, oldGeneration)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("release calls before rebound release = %d, want 1", got)
	}
	if err := result.binding.Release(); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("release calls = %d, want 2", got)
	}
}

type sessionBlockingReleaseRunner struct {
	blockedSession string
	started        chan struct{}
	unblock        chan struct{}
}

func (*sessionBlockingReleaseRunner) Run(context.Context, hookcmd.Event) (hookcmd.Result, error) {
	return hookcmd.Result{}, nil
}

func (r *sessionBlockingReleaseRunner) ReleaseSession(sessionID string) {
	if sessionID == r.blockedSession {
		close(r.started)
		<-r.unblock
	}
}

func TestBlockedSessionCleanupDoesNotBlockAnotherSessionBind(t *testing.T) {
	runner := &sessionBlockingReleaseRunner{blockedSession: "session-a", started: make(chan struct{}), unblock: make(chan struct{})}
	service, err := New(runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	first, err := service.Bind(t.Context(), BindingScope{SessionID: runner.blockedSession, Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- first.Release() }()
	<-runner.started

	type bindResult struct {
		binding *Binding
		err     error
	}
	bound := make(chan bindResult, 1)
	go func() {
		binding, bindErr := service.Bind(t.Context(), BindingScope{SessionID: "session-b", Provider: "provider", CWD: t.TempDir()})
		bound <- bindResult{binding: binding, err: bindErr}
	}()
	select {
	case result := <-bound:
		if result.err != nil {
			close(runner.unblock)
			<-releaseDone
			t.Fatal(result.err)
		}
		if err := result.binding.Release(); err != nil {
			close(runner.unblock)
			<-releaseDone
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(runner.unblock)
		<-releaseDone
		t.Fatal("session B bind blocked behind session A cleanup")
	}
	close(runner.unblock)
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
}
