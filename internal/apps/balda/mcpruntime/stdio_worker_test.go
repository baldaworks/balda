package mcpruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type pendingStdioLauncher struct{ launched chan struct{} }

type pendingStdioProcess struct {
	done chan struct{}
	once sync.Once
}

type controlledRestartTimer struct {
	fired   chan time.Time
	stopped atomic.Bool
}

func (t *controlledRestartTimer) Chan() <-chan time.Time { return t.fired }
func (t *controlledRestartTimer) Stop() bool             { return !t.stopped.Swap(true) }

type restartObservation struct {
	delay time.Duration
	timer *controlledRestartTimer
}
type controlledWorkerClock struct {
	seconds atomic.Int64
	timers  chan restartObservation
}

func (c *controlledWorkerClock) policy() stdioPolicy {
	return stdioPolicy{setupTimeout: 30 * time.Millisecond, stopTimeout: time.Second, stableAfter: time.Minute,
		now: func() time.Time { return time.Unix(c.seconds.Load(), 0) },
		newTimer: func(delay time.Duration) restartTimer {
			timer := &controlledRestartTimer{fired: make(chan time.Time, 1)}
			c.timers <- restartObservation{delay: delay, timer: timer}
			return timer
		}}
}
func (c *controlledWorkerClock) next(t *testing.T, expected time.Duration) *controlledRestartTimer {
	t.Helper()
	select {
	case next := <-c.timers:
		if next.delay != expected {
			t.Fatalf("restart delay=%v want%v", next.delay, expected)
		}
		return next.timer
	case <-time.After(time.Second):
		t.Fatal("worker did not schedule restart")
		return nil
	}
}

type workerFixtureProcess struct {
	done, discovered chan struct{}
	hung             bool
	stops            atomic.Int32
	once             sync.Once
}

func newWorkerFixtureProcess(hung bool) *workerFixtureProcess {
	return &workerFixtureProcess{done: make(chan struct{}), discovered: make(chan struct{}), hung: hung}
}
func (p *workerFixtureProcess) Discover(ctx context.Context) ([]Tool, error) {
	close(p.discovered)
	if p.hung {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []Tool{{Name: "fixture"}}, nil
}
func (p *workerFixtureProcess) Done() <-chan struct{} { return p.done }
func (p *workerFixtureProcess) Stop(context.Context) error {
	p.stops.Add(1)
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *workerFixtureProcess) exit() { p.once.Do(func() { close(p.done) }) }

type workerFixtureLauncher struct {
	calls  atomic.Int32
	launch func(context.Context, int) (Process, error)
}

func (*workerFixtureLauncher) Start(context.Context, InstanceKey, LaunchConfig) (Instance, error) {
	return nil, errors.New("explicit probe not used")
}
func (l *workerFixtureLauncher) Launch(ctx context.Context, _ InstanceKey, _ LaunchConfig) (Process, error) {
	return l.launch(ctx, int(l.calls.Add(1)))
}

func workerFixture(t *testing.T, launcher *workerFixtureLauncher, clock *controlledWorkerClock) *Reconciler {
	t.Helper()
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r.stdio = clock.policy()
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
	return r
}

func TestStdioLaunchFailuresBackOffAndShutdownCancelsTimer(t *testing.T) {
	clock := &controlledWorkerClock{timers: make(chan restartObservation, 1)}
	launcher := &workerFixtureLauncher{launch: func(context.Context, int) (Process, error) { return nil, errors.New("unavailable executable") }}
	r := workerFixture(t, launcher, clock)
	r.Reconcile(t.Context(), mcpSnapshot("stdio", mcpDescriptor("failure")))
	var timer *controlledRestartTimer
	for index, expected := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		timer = clock.next(t, expected)
		if got := launcher.calls.Load(); got != int32(index+1) {
			t.Fatalf("restart spun: calls=%d", got)
		}
		if index < 6 {
			timer.fired <- time.Unix(0, 0)
		}
	}
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !timer.stopped.Load() {
		t.Fatal("shutdown retained restart timer")
	}
	timer.fired <- time.Unix(0, 0)
	if launcher.calls.Load() != 7 {
		t.Fatal("shutdown attempted another launch")
	}
}

func TestStdioStableRunResetsRestartDelay(t *testing.T) {
	clock := &controlledWorkerClock{timers: make(chan restartObservation, 1)}
	process := newWorkerFixtureProcess(false)
	launcher := &workerFixtureLauncher{launch: func(_ context.Context, attempt int) (Process, error) {
		if attempt == 3 {
			return process, nil
		}
		return nil, errors.New("launch failed")
	}}
	r := workerFixture(t, launcher, clock)
	r.Reconcile(t.Context(), mcpSnapshot("stdio", mcpDescriptor("stable")))
	clock.next(t, time.Second).fired <- time.Unix(0, 0)
	clock.next(t, 2*time.Second).fired <- time.Unix(0, 0)
	<-process.discovered
	clock.seconds.Store(61)
	process.exit()
	timer := clock.next(t, time.Second)
	if process.stops.Load() != 1 {
		t.Fatal("exited process was not reaped before retry")
	}
	timer.fired <- time.Unix(61, 0)
	clock.next(t, 2*time.Second)
}

func TestStdioHungDiscoveryStopsChildBeforeDelayedRetry(t *testing.T) {
	clock := &controlledWorkerClock{timers: make(chan restartObservation, 1)}
	process := newWorkerFixtureProcess(true)
	launcher := &workerFixtureLauncher{launch: func(context.Context, int) (Process, error) { return process, nil }}
	r := workerFixture(t, launcher, clock)
	r.Reconcile(t.Context(), mcpSnapshot("stdio", mcpDescriptor("hung")))
	clock.next(t, time.Second)
	if process.stops.Load() != 1 {
		t.Fatal("hung discovery scheduled retry without stopping its child")
	}
	select {
	case <-process.done:
	default:
		t.Fatal("hung child was not reaped")
	}
	if health := r.Health(); len(health) != 1 || health[0].State != HealthFailed || health[0].ToolCount != 0 {
		t.Fatalf("failed discovery claimed readiness: %v", health)
	}
}

func TestStdioCancelDuringLaunchRetainsHandleUntilReaped(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "last lease", true: "shutdown"}[shutdown], func(t *testing.T) {
			clock := &controlledWorkerClock{timers: make(chan restartObservation, 1)}
			entered, canceled, finishLaunch := make(chan struct{}), make(chan struct{}), make(chan struct{})
			process := newWorkerFixtureProcess(false)
			launcher := &workerFixtureLauncher{launch: func(ctx context.Context, _ int) (Process, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-finishLaunch
				return process, nil
			}}
			r := workerFixture(t, launcher, clock)
			_, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{mcpDescriptor("launch race")})
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			done := make(chan error, 1)
			go func() {
				if shutdown {
					done <- r.Shutdown(t.Context())
				} else {
					release()
					done <- nil
				}
			}()
			<-canceled
			close(finishLaunch)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if process.stops.Load() != 1 || launcher.calls.Load() != 1 || len(r.Health()) != 0 {
				t.Fatalf("launch race lost ownership: stops=%d calls=%d health=%v", process.stops.Load(), launcher.calls.Load(), r.Health())
			}
			select {
			case <-process.done:
			default:
				t.Fatal("late-launched child not reaped")
			}
			select {
			case <-clock.timers:
				t.Fatal("canceled launch scheduled restart")
			default:
			}
		})
	}
}

func (*pendingStdioProcess) Discover(ctx context.Context) ([]Tool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *pendingStdioProcess) Done() <-chan struct{} { return p.done }
func (p *pendingStdioProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (l *pendingStdioLauncher) Launch(context.Context, InstanceKey, LaunchConfig) (Process, error) {
	close(l.launched)
	return &pendingStdioProcess{done: make(chan struct{})}, nil
}

func (l *pendingStdioLauncher) Start(ctx context.Context, _ InstanceKey, _ LaunchConfig) (Instance, error) {
	close(l.launched)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestStdioHandshakeDoesNotBlockLocalPublication(t *testing.T) {
	launcher := &pendingStdioLauncher{launched: make(chan struct{})}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	r.Reconcile(ctx, mcpSnapshot("stdio", mcpDescriptor("hung")))
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Errorf("publication waited for stdio handshake: %v", elapsed)
	}
	select {
	case <-launcher.launched:
	case <-ctx.Done():
		t.Error("startup did not schedule stdio launch")
	}
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
