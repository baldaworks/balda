package mcpruntime

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestFailureReadIsBoundedAndDoesNotAcquire(t *testing.T) {
	for _, reason := range []FailureReason{FailureAuthorizationRequired, FailureAuthorizationChallenge, FailureCaptureRequired, "private-unknown"} {
		t.Run(string(reason), func(t *testing.T) {
			descriptor := mcpDescriptor("blocked")
			key := keyFromDescriptor(descriptor)
			launcher := &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance)}
			projector := &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}
			r, err := New(fakeLaunchResolver{causes: map[InstanceKey]error{key: &LaunchError{Reason: reason}}}, launcher, projector, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, found := r.Failure(key); found {
				t.Fatal("read acquired an unobserved attachment")
			}
			r.Reconcile(t.Context(), mcpSnapshot("current", descriptor))
			got, found := r.Failure(key)
			if !found || got != boundedReason(reason) {
				t.Fatalf("failure = %s/%t, want bounded current reason", got, found)
			}
			if len(launcher.instances) != 0 {
				t.Fatal("failure read launched an attachment")
			}
			r.Reconcile(t.Context(), mcpSnapshot("next"))
			if _, found := r.Failure(key); found {
				t.Fatal("historical failure remained current")
			}
		})
	}
}

func TestFailureReadReadyAndUnknownAttachments(t *testing.T) {
	descriptor := mcpDescriptor("ready")
	key := keyFromDescriptor(descriptor)
	r, err := New(fakeLaunchResolver{causes: map[InstanceKey]error{}}, &fakeLauncher{instances: make(map[InstanceKey]*fakeInstance)}, &fakeProjector{outcome: runtimecatalogcmd.MCPProjectionApplied, set: make(map[InstanceKey]int), removed: make(map[InstanceKey]int)}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r.Reconcile(t.Context(), mcpSnapshot("current", descriptor))
	if _, found := r.Failure(key); found {
		t.Fatal("ready attachment exposed a recovery failure")
	}
	key.Revision += "-missing"
	if _, found := r.Failure(key); found {
		t.Fatal("unknown attachment exposed a failure")
	}
}
