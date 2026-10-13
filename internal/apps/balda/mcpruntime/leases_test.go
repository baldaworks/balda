package mcpruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const (
	leaseHTTPTransport = "streamable-http"
	leaseSSETransport  = "sse"
	leaseFirstName     = "first"
	leaseSecondName    = "second"
	leaseStaleName     = "stale"
	leaseLatestName    = "latest"
)

type unavailableLauncher struct{ starts int }

type retiringProjector struct{ entered, release, canceled chan struct{} }

func (p *retiringProjector) Project(ctx context.Context, _ InstanceKey, _ LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error) {
	context.AfterFunc(ctx, func() { close(p.canceled) })
	close(p.entered)
	<-p.release
	return runtimecatalogcmd.MCPProjectionApplied, nil
}
func (*retiringProjector) Remove(context.Context, InstanceKey) error { return nil }

func TestRetiringInstallationRefusesNewLeaseBeforeCleanupCompletes(t *testing.T) {
	descriptor := mcpDescriptor("retiring")
	descriptor.Transport = leaseHTTPTransport
	projector := &retiringProjector{entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	r, err := New(fakeLaunchResolver{}, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	installed := make(chan struct{})
	go func() { defer close(installed); r.Reconcile(t.Context(), mcpSnapshot("current", descriptor)) }()
	<-projector.entered
	stopped := make(chan error, 1)
	go func() { r.Reconcile(t.Context(), mcpSnapshot("empty")); stopped <- nil }()
	<-projector.canceled
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
	defer cancel()
	keys, release, err := r.AcquireDescriptors(ctx, []runtimecatalogcmd.MCPServerDescriptor{descriptor})
	if keys != nil || release != nil || err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("retiring installation admitted/waited for a new lease: keys=%v err=%v", keys, err)
	}
	close(projector.release)
	<-installed
	if err := <-stopped; err != nil || len(r.Health()) != 0 {
		t.Fatalf("retiring cleanup leaked: %v health=%v", err, r.Health())
	}
}

type desiredResolver struct{ entered, release chan struct{} }

func (r *desiredResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if descriptor.Name == leaseFirstName {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return LaunchConfig{}, ctx.Err()
		}
	}
	return LaunchConfig{Transport: descriptor.Transport}, nil
}

func TestPublicationMarksCompleteDesiredSetBeforeInstallingFirstEntry(t *testing.T) {
	first, second := mcpDescriptor(leaseFirstName), mcpDescriptor(leaseSecondName)
	first.Name, first.ID.Name, first.Transport = leaseFirstName, leaseFirstName, leaseHTTPTransport
	second.Name, second.ID.Name, second.Transport = leaseSecondName, leaseSecondName, leaseHTTPTransport
	resolver := &desiredResolver{entered: make(chan struct{}), release: make(chan struct{})}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(resolver, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r.Reconcile(t.Context(), mcpSnapshot("initial", second))
	_, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); r.Reconcile(t.Context(), mcpSnapshot("complete", first, second)) }()
	<-resolver.entered
	release()
	removed := projector.removed[keyFromDescriptor(second)]
	close(resolver.release)
	<-done
	if removed != 0 || projector.set[keyFromDescriptor(second)] != 1 {
		t.Fatalf("still-desired later entry drained/reinstalled: removals=%d installations=%d", removed, projector.set[keyFromDescriptor(second)])
	}
}

type heldCleanupProjector struct {
	removing, release, latest chan struct{}
	oldCalls                  atomic.Int32
}

func (p *heldCleanupProjector) Project(_ context.Context, key InstanceKey, _ LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error) {
	if key.Name == leaseFirstName {
		p.oldCalls.Add(1)
		return runtimecatalogcmd.MCPProjectionUnsupported, nil
	}
	close(p.latest)
	return runtimecatalogcmd.MCPProjectionApplied, nil
}
func (p *heldCleanupProjector) Remove(_ context.Context, key InstanceKey) error {
	if key.Name == leaseFirstName {
		close(p.removing)
		<-p.release
	}
	return nil
}

func TestNewerPublicationSupersedesFailedEntryCleanup(t *testing.T) {
	old, latest := mcpDescriptor(leaseFirstName), mcpDescriptor(leaseLatestName)
	old.Name, old.ID.Name, old.Transport = leaseFirstName, leaseFirstName, leaseHTTPTransport
	latest.Name, latest.ID.Name, latest.Transport = leaseLatestName, leaseLatestName, leaseHTTPTransport
	projector := &heldCleanupProjector{removing: make(chan struct{}), release: make(chan struct{}), latest: make(chan struct{})}
	r, err := New(fakeLaunchResolver{}, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r.Reconcile(t.Context(), mcpSnapshot("failed", old))
	retried, published := make(chan struct{}), make(chan struct{})
	go func() { defer close(retried); r.Reconcile(t.Context(), mcpSnapshot("retry", old)) }()
	<-projector.removing
	go func() { defer close(published); r.Reconcile(t.Context(), mcpSnapshot("latest", latest)) }()
	<-projector.latest
	close(projector.release)
	<-retried
	<-published
	health := r.Health()
	if projector.oldCalls.Load() != 1 || len(health) != 1 || health[0].Key != keyFromDescriptor(latest) {
		t.Fatalf("obsolete failed-entry retry resurrected config: oldCalls=%d health=%v", projector.oldCalls.Load(), health)
	}
}

func (l *unavailableLauncher) Start(context.Context, InstanceKey, LaunchConfig) (Instance, error) {
	l.starts++
	return nil, errors.New("unreachable remote")
}

type heldLocalResolver struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

type shutdownInstallResolver struct {
	entered chan struct{}
	calls   atomic.Int32
}

type supersededResolver struct {
	entered chan struct{}
	stale   atomic.Int32
}

func (r *supersededResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if descriptor.Name == leaseFirstName {
		close(r.entered)
		<-ctx.Done()
		return LaunchConfig{}, ctx.Err()
	}
	if descriptor.Name == leaseStaleName {
		r.stale.Add(1)
	}
	return LaunchConfig{Transport: descriptor.Transport}, nil
}

func TestNewerPublicationSupersedesPendingDesiredSet(t *testing.T) {
	first, stale, latest := mcpDescriptor(leaseFirstName), mcpDescriptor(leaseStaleName), mcpDescriptor(leaseLatestName)
	first.Name, first.ID.Name, first.Transport = leaseFirstName, leaseFirstName, leaseHTTPTransport
	stale.Name, stale.ID.Name, stale.Transport = leaseStaleName, leaseStaleName, leaseHTTPTransport
	latest.Name, latest.ID.Name, latest.Transport = leaseLatestName, leaseLatestName, leaseHTTPTransport
	resolver := &supersededResolver{entered: make(chan struct{})}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(resolver, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); r.Reconcile(t.Context(), mcpSnapshot("superseded", first, stale)) }()
	<-resolver.entered
	r.Reconcile(t.Context(), mcpSnapshot(leaseLatestName, latest))
	<-done
	health := r.Health()
	if resolver.stale.Load() != 0 || len(health) != 1 || health[0].Key != keyFromDescriptor(latest) {
		t.Fatalf("superseded publication resurrected desired config: health=%v stale=%d", health, resolver.stale.Load())
	}
}

func (r *shutdownInstallResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if r.calls.Add(1) == 1 {
		close(r.entered)
		<-ctx.Done()
		return LaunchConfig{}, ctx.Err()
	}
	return LaunchConfig{Transport: descriptor.Transport}, nil
}

func TestShutdownPreventsPendingSelectionFromInstallingAnotherDescriptor(t *testing.T) {
	for _, acquire := range []bool{false, true} {
		t.Run(map[bool]string{false: "publication", true: "acquisition"}[acquire], func(t *testing.T) {
			first, second := mcpDescriptor(leaseFirstName), mcpDescriptor(leaseSecondName)
			first.Name, first.ID.Name, first.Transport = leaseFirstName, leaseFirstName, leaseHTTPTransport
			second.Name, second.ID.Name, second.Transport = leaseSecondName, leaseSecondName, leaseSSETransport
			resolver := &shutdownInstallResolver{entered: make(chan struct{})}
			projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
			r, err := New(resolver, &unavailableLauncher{}, projector, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if acquire {
					_, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{first, second})
					if release != nil {
						release()
						done <- errors.New("pending acquisition succeeded across shutdown")
						return
					}
					done <- err
				} else {
					r.Reconcile(t.Context(), mcpSnapshot("pending", first, second))
					done <- nil
				}
			}()
			<-resolver.entered
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := r.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if acquire && err == nil {
					t.Fatal("shutdown acquisition lost its failure")
				}
			case <-ctx.Done():
				t.Fatal("pending operation did not drain")
			}
			if resolver.calls.Load() != 1 || len(r.Health()) != 0 || len(projector.set) != 0 {
				t.Fatalf("shutdown resurrected config: calls=%d health=%v projected=%v", resolver.calls.Load(), r.Health(), projector.set)
			}
		})
	}
}

func (r *heldLocalResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	r.calls.Add(1)
	close(r.entered)
	select {
	case <-r.release:
		return LaunchConfig{Transport: descriptor.Transport}, nil
	case <-ctx.Done():
		return LaunchConfig{}, ctx.Err()
	}
}

func TestConfigurationInstallDoesNotBlockHealthOrCanceledWaiters(t *testing.T) {
	descriptor := mcpDescriptor("remote")
	descriptor.Transport = leaseHTTPTransport
	resolver := &heldLocalResolver{entered: make(chan struct{}), release: make(chan struct{})}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(resolver, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); r.Reconcile(t.Context(), mcpSnapshot("current", descriptor)) }()
	<-resolver.entered
	if health := r.Health(); len(health) != 1 || health[0].State != HealthStarting {
		t.Fatalf("pending local installation health = %+v", health)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, release, err := r.AcquireDescriptors(ctx, []runtimecatalogcmd.MCPServerDescriptor{descriptor})
	if release != nil || !errors.Is(err, context.DeadlineExceeded) || resolver.calls.Load() != 1 {
		t.Fatalf("waiter fanout/deadline: %v calls=%d", err, resolver.calls.Load())
	}
	close(resolver.release)
	<-done
	keys, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{descriptor})
	if err != nil || len(keys) != 1 {
		t.Fatalf("later exact lease = %v", err)
	}
	release()
	if err := r.Shutdown(t.Context()); err != nil || len(r.Health()) != 0 {
		t.Fatalf("shutdown failed: %v", err)
	}
}

func TestConfigurationLeaseRollsBackAllNewReferencesOnStructuralFailure(t *testing.T) {
	good, bad := mcpDescriptor("good"), mcpDescriptor("bad")
	good.Transport, bad.Transport = leaseHTTPTransport, leaseSSETransport
	bad.ID.Name, bad.Name = "bad", "bad"
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(fakeLaunchResolver{causes: map[InstanceKey]error{keyFromDescriptor(bad): runtimecatalogcmd.ErrRevisionUnavailable}}, &unavailableLauncher{}, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	keys, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{good, bad})
	if err == nil || keys != nil || release != nil || len(r.Health()) != 0 || projector.removed[keyFromDescriptor(good)] != 1 {
		t.Fatalf("partial structural acquisition leaked: keys=%v err=%v health=%v", keys, err, r.Health())
	}
}

func TestConfigurationLeasesPreserveUnavailableExactSelectionWithoutLaunching(t *testing.T) {
	first, second := mcpDescriptor(leaseFirstName), mcpDescriptor(leaseSecondName)
	first.Transport, second.Transport = leaseHTTPTransport, leaseSSETransport
	second.ID.Name, second.Name = leaseSecondName, leaseSecondName
	launcher := &unavailableLauncher{}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionNewRuntimesOnly, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	r, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r.Reconcile(t.Context(), mcpSnapshot("full", first, second))
	keys, release, err := r.AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{first, second})
	if err != nil {
		t.Fatalf("unavailable exact selection cannot lease: %v", err)
	}
	if len(keys) != 2 || keys[0] != keyFromDescriptor(first) || keys[1] != keyFromDescriptor(second) || launcher.starts != 0 {
		t.Fatalf("selection or network attempts changed: keys=%v starts=%d", keys, launcher.starts)
	}
	r.Reconcile(t.Context(), mcpSnapshot("empty"))
	if len(projector.removed) != 0 {
		t.Fatal("publication removed leased revisions")
	}
	release()
	release()
	if len(r.Health()) != 0 || len(projector.removed) != 2 {
		t.Fatal("idempotent release did not drain exact projections")
	}
}
