package mcpruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type fakeLaunchResolver struct {
	fail map[InstanceKey]bool
}

func (r fakeLaunchResolver) ResolveLaunch(_ context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if r.fail[keyFromDescriptor(descriptor)] {
		return LaunchConfig{}, errors.New("secret value must not escape")
	}
	return LaunchConfig{Transport: descriptor.Transport, Command: "server"}, nil
}

type fakeInstance struct {
	tools            []Tool
	nextTools        []Tool
	toolCalls        int
	closed           int
	closeErr         error
	closeHadDeadline bool
}

func (i *fakeInstance) Tools() []Tool {
	i.toolCalls++
	if i.toolCalls > 1 && i.nextTools != nil {
		return append([]Tool(nil), i.nextTools...)
	}
	return append([]Tool(nil), i.tools...)
}
func (i *fakeInstance) Close(ctx context.Context) error {
	i.closed++
	_, i.closeHadDeadline = ctx.Deadline()
	return i.closeErr
}

type fakeLauncher struct {
	instances map[InstanceKey]*fakeInstance
	fail      map[InstanceKey]bool
}

func (l *fakeLauncher) Start(_ context.Context, key InstanceKey, _ LaunchConfig) (Instance, error) {
	if l.fail[key] {
		return nil, errors.New("private process error")
	}
	instance := &fakeInstance{tools: []Tool{{Name: "inspect"}}}
	l.instances[key] = instance
	return instance, nil
}

type fakeProjector struct {
	outcome   runtimecatalogcmd.MCPProjectionOutcome
	set       map[InstanceKey]int
	removed   map[InstanceKey]int
	removeErr error
}

func (p *fakeProjector) Project(_ context.Context, key InstanceKey, _ LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error) {
	p.set[key]++
	return p.outcome, nil
}

func (p *fakeProjector) Remove(_ context.Context, key InstanceKey) error {
	p.removed[key]++
	return p.removeErr
}

func TestReconcilerKeepsPinnedOldRevisionUntilTurnDrain(t *testing.T) {
	t.Parallel()

	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	oldDescriptor := mcpDescriptor("revision-old")
	newDescriptor := mcpDescriptor("revision-new")
	oldSnapshot := mcpSnapshot("snapshot-old", oldDescriptor)
	newSnapshot := mcpSnapshot("snapshot-new", newDescriptor)
	if health := reconciler.Reconcile(context.Background(), oldSnapshot); len(health) != 1 || health[0].State != HealthReady {
		t.Fatalf("old health = %+v, want ready", health)
	}
	oldKey := keyFromDescriptor(oldDescriptor)
	tools, release, err := reconciler.Acquire([]InstanceKey{oldKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Key != oldKey {
		t.Fatalf("old tools = %+v, want revision-qualified tool", tools)
	}
	if health := reconciler.Reconcile(context.Background(), newSnapshot); len(health) != 2 {
		t.Fatalf("upgrade health = %+v, want old and new revisions", health)
	}
	if launcher.instances[oldKey].closed != 0 {
		t.Fatal("old pinned instance closed before turn drain")
	}
	release()
	if launcher.instances[oldKey].closed != 1 || projector.removed[oldKey] != 1 {
		t.Fatalf("old close/remove = %d/%d, want drained once", launcher.instances[oldKey].closed, projector.removed[oldKey])
	}
	if _, _, err := reconciler.Acquire([]InstanceKey{oldKey}); err == nil {
		t.Fatal("Acquire(old) error = nil after drain")
	}
	if newSnapshot.ID != "snapshot-new" {
		t.Fatalf("observed health mutated snapshot identity: %q", newSnapshot.ID)
	}
}

func TestReconcilerIsolatesFailureAndReportsProjectionOutcome(t *testing.T) {
	t.Parallel()

	failed := mcpDescriptor("revision-failed")
	failed.Name = "failed"
	failed.ID.Name = "failed"
	ready := mcpDescriptor("revision-ready")
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: map[InstanceKey]bool{keyFromDescriptor(failed): true}}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionNewRuntimesOnly, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", failed, ready))
	if len(health) != 2 {
		t.Fatalf("health = %+v, want isolated statuses", health)
	}
	states := map[string]Health{}
	for _, status := range health {
		states[status.Key.Name] = status
	}
	if states["failed"].State != HealthFailed || states["failed"].ErrorClass != "start" {
		t.Fatalf("failed status = %+v", states["failed"])
	}
	if states["server"].State != HealthReady || states["server"].Outcome != runtimecatalogcmd.MCPProjectionNewRuntimesOnly {
		t.Fatalf("ready status = %+v", states["server"])
	}
}

func TestReconcilerDoesNotRestartForRebuildRequiredProjection(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionRebuildRequired, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	if len(health) != 1 || health[0].State != HealthDegraded || health[0].Outcome != runtimecatalogcmd.MCPProjectionRebuildRequired {
		t.Fatalf("health = %+v, want explicit rebuild-required degradation", health)
	}
	if len(launcher.instances) != 1 {
		t.Fatalf("launcher starts = %d, want no implicit restart", len(launcher.instances))
	}
}

func TestReconcilerRetainsReadyRevisionWhenReplacementFails(t *testing.T) {
	t.Parallel()

	oldDescriptor := mcpDescriptor("revision-old")
	newDescriptor := mcpDescriptor("revision-new")
	launcher := &fakeLauncher{
		instances: make(map[InstanceKey]*fakeInstance),
		fail:      map[InstanceKey]bool{keyFromDescriptor(newDescriptor): true},
	}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(context.Background(), mcpSnapshot("old", oldDescriptor))
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("new", newDescriptor))
	if len(health) != 2 {
		t.Fatalf("health = %+v, want ready old and failed replacement", health)
	}
	oldKey := keyFromDescriptor(oldDescriptor)
	if launcher.instances[oldKey].closed != 0 || projector.removed[oldKey] != 0 {
		t.Fatal("ready old revision drained before replacement became ready")
	}
	tools, release, err := reconciler.Acquire([]InstanceKey{oldKey})
	if err != nil || len(tools) != 1 {
		t.Fatalf("Acquire(old) = %+v, %v; want retained ready revision", tools, err)
	}
	release()
}

func TestReconcilerRejectsExcessiveToolMetadata(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	key := keyFromDescriptor(descriptor)
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{MaxServers: 1, MaxTools: 1, MaxToolMetadataBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	if len(health) != 1 || health[0].State != HealthFailed || health[0].ErrorClass != "tool_limit" {
		t.Fatalf("health = %+v, want bounded tool failure", health)
	}
	if launcher.instances[key].closed != 1 || projector.set[key] != 0 {
		t.Fatal("oversized tool metadata reached projection")
	}
}

func TestReconcilerShutdownRemovesProjectionAndClosesInstance(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	key := keyFromDescriptor(descriptor)
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	if err := reconciler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if launcher.instances[key].closed != 1 || projector.removed[key] != 1 || len(reconciler.Health()) != 0 {
		t.Fatalf("shutdown close/remove/health = %d/%d/%+v", launcher.instances[key].closed, projector.removed[key], reconciler.Health())
	}
}

func TestReconcilerCachesBoundedHandshakeTools(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	key := keyFromDescriptor(descriptor)
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	launcher.instances[key].nextTools = []Tool{{Name: "changed-after-handshake"}, {Name: "extra"}}
	tools, release, err := reconciler.Acquire([]InstanceKey{key})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(tools) != 1 || tools[0].Name != "inspect" || launcher.instances[key].toolCalls != 1 {
		t.Fatalf("Acquire() tools = %+v, calls = %d; want cached handshake set", tools, launcher.instances[key].toolCalls)
	}
}

func TestReconcilerFailsClosedForUnknownProjectionOutcome(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionOutcome("future"), set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	if len(health) != 1 || health[0].State != HealthDegraded || health[0].ErrorClass != "projection" {
		t.Fatalf("health = %+v, want fail-closed projection degradation", health)
	}
}

func TestReconcilerRetainsFailedCleanupForRetry(t *testing.T) {
	t.Parallel()

	descriptor := mcpDescriptor("revision")
	key := keyFromDescriptor(descriptor)
	launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance), fail: make(map[InstanceKey]bool)}
	projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
	reconciler, err := New(fakeLaunchResolver{}, launcher, projector, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(context.Background(), mcpSnapshot("snapshot", descriptor))
	launcher.instances[key].closeErr = errors.New("private close failure")
	projector.removeErr = errors.New("private remove failure")
	health := reconciler.Reconcile(context.Background(), mcpSnapshot("empty"))
	if len(health) != 1 || health[0].State != HealthStopping || health[0].ErrorClass != errorClassStop {
		t.Fatalf("failed cleanup health = %+v", health)
	}
	if !launcher.instances[key].closeHadDeadline {
		t.Fatal("cleanup Close context has no host deadline")
	}
	launcher.instances[key].closeErr = nil
	projector.removeErr = nil
	if health := reconciler.Reconcile(context.Background(), mcpSnapshot("empty")); len(health) != 0 {
		t.Fatalf("retry cleanup health = %+v, want removed", health)
	}
}

func mcpDescriptor(revision runtimecatalogcmd.RevisionID) runtimecatalogcmd.MCPServerDescriptor {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
	return runtimecatalogcmd.MCPServerDescriptor{
		ID:       runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: "server"},
		Revision: revision, Name: "server", Transport: "stdio", ConfigRef: "mcp.json",
	}
}

func mcpSnapshot(id runtimecatalogcmd.SnapshotID, descriptors ...runtimecatalogcmd.MCPServerDescriptor) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{ID: id, MCPServers: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor)}
	for _, descriptor := range descriptors {
		snapshot.MCPServers[descriptor.ID] = descriptor
	}
	return snapshot
}
