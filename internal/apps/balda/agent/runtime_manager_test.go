package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

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
