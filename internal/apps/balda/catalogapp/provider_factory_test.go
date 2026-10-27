package catalogapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
)

type catalogTestControl struct{}

func (*catalogTestControl) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{}
}

func (*catalogTestControl) ValidateModel(context.Context, adksession.Session, string) agentproduct.ModelValidation {
	return agentproduct.ModelValidation{}
}

func (*catalogTestControl) ChangeModel(context.Context, adksession.Session, string) agentproduct.ModelChange {
	return agentproduct.ModelChange{}
}

func (*catalogTestControl) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	return agentproduct.ContextChange{}
}

func TestProviderFactoryConstructsCompleteUnavailableMCPShells(t *testing.T) {
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	configs := map[string]agentconfig.Config{
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture", Model: "fixture"}},
		"acp":    {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"/does/not/exist"}}},
	}
	registry := mcpregistry.New(map[string]agentconfig.MCPServerConfig{
		"revision-one": {Type: agentconfig.MCPServerTypeHTTP, URL: remote.URL + "/one", Headers: map[string]string{"X-Revision": "one"}},
		"revision-two": {Type: agentconfig.MCPServerTypeSSE, URL: remote.URL + "/two"},
	})
	factory := NewProviderFactory(configs, registry)
	for _, id := range []string{"hosted", "acp"} {
		t.Run(id, func(t *testing.T) {
			built, err := factory.BuildScoped(t.Context(), agentfactory.BuildRequest{AgentID: id, WorkingDirectory: t.TempDir()}, map[string][]string{id: {"revision-one", "revision-two"}})
			if err != nil {
				t.Fatalf("construct complete unavailable shell: %v", err)
			}
			if err := built.(io.Closer).Close(); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatal("provider construction contacted a selected remote")
			}
			if _, err := factory.BuildScoped(t.Context(), agentfactory.BuildRequest{AgentID: id, WorkingDirectory: t.TempDir()}, map[string][]string{id: {"revision-one", "missing-revision"}}); err == nil {
				t.Fatal("missing exact selected config accepted")
			}
		})
	}
}

func TestRuntimeProductWithReleaseClosesInReverseAcquisitionOrder(t *testing.T) {
	var order []string
	ag, err := adkagent.New(adkagent.Config{Name: "ordered"})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	product, err := baldaagent.NewRuntimeProduct(ag, nil, func() error {
		order = append(order, "product")
		return nil
	})
	if err != nil {
		t.Fatalf("NewRuntimeProduct() error = %v", err)
	}
	adapted, err := runtimeProductWithRelease(product, func() error {
		order = append(order, "native-hook")
		return nil
	})
	if err != nil {
		t.Fatalf("runtimeProductWithRelease() error = %v", err)
	}
	if err := adapted.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := adapted.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	want := []string{"product", "native-hook"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}

func TestCatalogCleanupPreservesPrimaryErrorAndBoundsCleanupDetails(t *testing.T) {
	primaryErr := errors.New("primary construction failure")
	productDetail := errors.New("private product cleanup detail")
	hookDetail := errors.New("private native hook detail")
	var productCalls, hookCalls int
	ag, err := adkagent.New(adkagent.Config{Name: "cleanup"})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	product, err := baldaagent.NewRuntimeProduct(ag, nil, func() error {
		productCalls++
		return productDetail
	})
	if err != nil {
		t.Fatalf("NewRuntimeProduct() error = %v", err)
	}
	cleanupErr := joinCatalogConstructionError(primaryErr, product, func() error {
		hookCalls++
		return hookDetail
	})
	for _, want := range []error{primaryErr, baldaagent.ErrRuntimeProductClose, ErrNativeHookRelease} {
		if !errors.Is(cleanupErr, want) {
			t.Fatalf("construction error = %v, want errors.Is(%v)", cleanupErr, want)
		}
	}
	for _, private := range []error{productDetail, hookDetail} {
		if strings.Contains(cleanupErr.Error(), private.Error()) {
			t.Fatalf("construction error leaked cleanup detail %q: %v", private, cleanupErr)
		}
	}
	if productCalls != 1 || hookCalls != 1 {
		t.Fatalf("cleanup calls product/hook = %d/%d, want 1/1", productCalls, hookCalls)
	}
}

func TestCatalogProductCloseReturnsStableBoundedClassifications(t *testing.T) {
	productDetail := errors.New("private product cleanup detail")
	hookDetail := errors.New("private native hook detail")
	ag, err := adkagent.New(adkagent.Config{Name: "cleanup"})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	product, err := baldaagent.NewRuntimeProduct(ag, nil, func() error { return productDetail })
	if err != nil {
		t.Fatalf("NewRuntimeProduct() error = %v", err)
	}
	adapted, err := runtimeProductWithRelease(product, func() error { return hookDetail })
	if err != nil {
		t.Fatalf("runtimeProductWithRelease() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		closeErr := adapted.Close()
		if !errors.Is(closeErr, baldaagent.ErrRuntimeProductClose) || !errors.Is(closeErr, ErrNativeHookRelease) {
			t.Fatalf("Close() error = %v, want bounded product and hook classifications", closeErr)
		}
		if strings.Contains(closeErr.Error(), productDetail.Error()) || strings.Contains(closeErr.Error(), hookDetail.Error()) {
			t.Fatalf("Close() leaked cleanup details: %v", closeErr)
		}
	}
}

func TestCatalogProductPreservesControlAndClosesConcurrentlyOnce(t *testing.T) {
	productDetail := errors.New("private product cleanup detail")
	hookDetail := errors.New("private native hook detail")
	control := &catalogTestControl{}
	ag, err := adkagent.New(adkagent.Config{Name: "concurrent-cleanup"})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	var productCalls, hookCalls atomic.Int32
	var orderMu sync.Mutex
	var order []string
	product, err := baldaagent.NewRuntimeProduct(ag, control, func() error {
		productCalls.Add(1)
		orderMu.Lock()
		order = append(order, "product")
		orderMu.Unlock()
		return productDetail
	})
	if err != nil {
		t.Fatalf("NewRuntimeProduct() error = %v", err)
	}
	adapted, err := runtimeProductWithRelease(product, func() error {
		hookCalls.Add(1)
		orderMu.Lock()
		order = append(order, "native-hook")
		orderMu.Unlock()
		return hookDetail
	})
	if err != nil {
		t.Fatalf("runtimeProductWithRelease() error = %v", err)
	}
	if adapted.Agent != ag || adapted.Control != control {
		t.Fatal("catalog adaptation lost the agent or optional SessionControl")
	}

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- adapted.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for closeErr := range errs {
		if !errors.Is(closeErr, baldaagent.ErrRuntimeProductClose) || !errors.Is(closeErr, ErrNativeHookRelease) {
			t.Errorf("Close() error = %v, want bounded product and hook classifications", closeErr)
		}
		if strings.Contains(closeErr.Error(), productDetail.Error()) || strings.Contains(closeErr.Error(), hookDetail.Error()) {
			t.Errorf("Close() leaked cleanup details: %v", closeErr)
		}
	}
	if productCalls.Load() != 1 || hookCalls.Load() != 1 {
		t.Fatalf("cleanup calls product/hook = %d/%d, want 1/1", productCalls.Load(), hookCalls.Load())
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	if got := strings.Join(order, ","); got != "product,native-hook" {
		t.Fatalf("close order = %q, want product,native-hook", got)
	}
}
