package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPinnedRuntimeMCPServerIDsPreservesHostConfiguration(t *testing.T) {
	t.Parallel()
	got := pinnedRuntimeMCPServerIDs(
		[]string{" host.one ", "shared", "host.one"},
		[]string{"snapshot.old", "shared"},
	)
	want := []string{"host.one", "shared", "snapshot.old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinnedRuntimeMCPServerIDs() = %#v, want %#v", got, want)
	}
}

type closeableRuntimeAgent struct {
	closeErr error
}

func (a *closeableRuntimeAgent) Close() error {
	return a.closeErr
}

func TestCloseRuntimeAgent_IgnoresExpectedShutdownError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close acp client: acp client close: context canceled")}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}

func TestCloseRuntimeAgent_ReturnsUnexpectedCloseError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed")}

	err := closeRuntimeAgent(agent)
	if err == nil {
		t.Fatal("closeRuntimeAgent() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "close balda runtime agent: close failed") {
		t.Fatalf("closeRuntimeAgent() error = %v, want wrapped close failure", err)
	}
}

func TestCloseRuntimeAgent_IgnoresWrappedContextCanceled(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed: %w", context.Canceled)}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}
