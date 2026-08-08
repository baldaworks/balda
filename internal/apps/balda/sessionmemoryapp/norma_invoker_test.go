package sessionmemoryapp

import (
	"context"
	"testing"
	"time"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
)

func TestNormaInvokerUsesBoundedIsolatedConfiguration(t *testing.T) {
	invoker, err := NewNormaInvoker(NormaInvokerConfig{
		Builder:    &baldaagent.Builder{},
		ProviderID: "memory-fast",
		WorkingDir: t.TempDir(),
		MaxBytes:   4096,
		Timeout:    75 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewNormaInvoker() error = %v", err)
	}
	if invoker.providerID != "memory-fast" {
		t.Fatalf("invoker provider ID = %q, want memory-fast", invoker.providerID)
	}
	if invoker.maxBytes != 4096 || invoker.timeout != 75*time.Millisecond {
		t.Fatalf("invoker bounds = max bytes %d, timeout %s; want 4096/75ms", invoker.maxBytes, invoker.timeout)
	}
	if invoker.runtime != nil {
		t.Fatal("invoker created provider runtime before first invocation")
	}
	if err := invoker.Close(context.Background()); err != nil {
		t.Fatalf("NormaInvoker.Close() error = %v", err)
	}
	if err := invoker.Close(context.Background()); err != nil {
		t.Fatalf("second NormaInvoker.Close() error = %v", err)
	}
}
