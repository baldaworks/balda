package hooknative

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

type testAdapter struct {
	name     string
	supports bool
	prepared agentconfig.Config
}

func (a *testAdapter) Name() string                     { return a.name }
func (a *testAdapter) Supports(agentconfig.Config) bool { return a.supports }
func (a *testAdapter) Prepare(context.Context, LeafRequest) (PreparedLeaf, error) {
	return PreparedLeaf{Config: a.prepared}, nil
}

func TestRegistryRequiresExactlyOneMatch(t *testing.T) {
	request := LeafRequest{LeafID: "worker", Config: agentconfig.Config{Type: agentconfig.AgentTypeGenericACP}}
	tests := []struct {
		name     string
		adapters []*testAdapter
		wantErr  error
	}{
		{name: "none", adapters: []*testAdapter{{name: "other"}}, wantErr: ErrNoAdapter},
		{name: "multiple", adapters: []*testAdapter{{name: "zeta", supports: true}, {name: "alpha", supports: true}}, wantErr: ErrMultipleAdapters},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapters := make([]Adapter, len(test.adapters))
			for i, adapter := range test.adapters {
				adapters[i] = adapter
			}
			registry, err := NewRegistry(adapters...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Prepare(t.Context(), request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Prepare() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == ErrNoAdapter && !errors.Is(err, hookcmd.ErrUnsupported) {
				t.Fatalf("Prepare() error = %v, want unsupported classification", err)
			}
			if test.wantErr == ErrMultipleAdapters && !errors.Is(err, hookcmd.ErrConflict) {
				t.Fatalf("Prepare() error = %v, want conflict classification", err)
			}
		})
	}
}

func TestRegistryRegistrationOrderDoesNotSelectWinner(t *testing.T) {
	request := LeafRequest{LeafID: "worker", Config: agentconfig.Config{Type: agentconfig.AgentTypeGenericACP}}
	for _, adapters := range [][]Adapter{
		{&testAdapter{name: "first", supports: true}, &testAdapter{name: "second", supports: true}},
		{&testAdapter{name: "second", supports: true}, &testAdapter{name: "first", supports: true}},
	} {
		registry, err := NewRegistry(adapters...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Prepare(t.Context(), request); !errors.Is(err, ErrMultipleAdapters) {
			t.Fatalf("Prepare() error = %v, want ambiguity", err)
		}
	}
}

func TestRegistryCopiesRegistrationSlice(t *testing.T) {
	adapter := &testAdapter{name: "only", supports: true, prepared: agentconfig.Config{Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"prepared"}}}}
	adapters := []Adapter{adapter}
	registry, err := NewRegistry(adapters...)
	if err != nil {
		t.Fatal(err)
	}
	adapters[0] = &testAdapter{name: "replacement"}
	prepared, err := registry.Prepare(t.Context(), LeafRequest{LeafID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prepared.Config.GenericACP.Cmd, adapter.prepared.GenericACP.Cmd) || prepared.Config.Type != adapter.prepared.Type {
		t.Fatalf("prepared config = %+v, want %+v", prepared.Config, adapter.prepared)
	}
}
