package hooknativeapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/claude"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativeapp"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/normahq/runtime/v2/agentconfig"
)

type store struct{ record hookcmd.Record }

func (s store) Load(context.Context) (hookcmd.Record, error) { return s.record, nil }
func (s store) Save(context.Context, hookcmd.Record, int64) (hookcmd.Record, error) {
	return hookcmd.Record{}, errors.New("unused")
}

func TestActivationRequiresPreparedSession(t *testing.T) {
	definition := hookcmd.Definition{Name: "guard", Event: hookcmd.PreToolUse, Enabled: true, Command: "true", TimeoutSeconds: 1}
	record := hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}
	provider := agentconfig.Config{Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{"fixture"}}}
	service, err := hooknativeapp.New(hooks.NewRunner(store{record}), store{record}, "provider", map[string]agentconfig.Config{"provider": provider}, claude.New)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if err := service.VerifySession(t.Context(), "session"); !errors.Is(err, hookcmd.ErrSessionReset) {
		t.Fatalf("before preparation = %v", err)
	}
	prepared, err := service.Prepare(t.Context(), hooknative.LeafRequest{SessionID: "session", ProviderID: "provider", LeafID: "provider", CWD: t.TempDir(), Config: provider})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySession(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Release(); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySession(t.Context(), "session"); !errors.Is(err, hookcmd.ErrSessionReset) {
		t.Fatalf("after release = %v", err)
	}
}

func TestDisabledHooksDoNotRequireNativePreparation(t *testing.T) {
	service, err := hooknativeapp.New(hooks.NewRunner(store{}), store{}, "provider", map[string]agentconfig.Config{}, claude.New)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	required, err := service.Required(t.Context(), "provider")
	if err != nil || required {
		t.Fatalf("Required = %v, %v", required, err)
	}
	if err := service.VerifySession(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
}

func TestValidationRejectsUnsupportedDirectACP(t *testing.T) {
	record := hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	service, err := hooknativeapp.New(hooks.NewRunner(store{record}), store{record}, "provider", map[string]agentconfig.Config{"provider": {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"fixture"}}}}, claude.New)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	err = service.ValidateDefinition(t.Context(), hookcmd.Definition{Name: "guard", Event: hookcmd.PreToolUse, Enabled: true})
	if !errors.Is(err, hookcmd.ErrUnsupported) {
		t.Fatalf("generic ACP validation = %v", err)
	}
}
