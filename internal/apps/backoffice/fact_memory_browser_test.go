package backoffice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/memorybackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const browserImportedFact = "Synthetic imported memory:\n  Preserve this whole entry.\nSecond paragraph stays together."

// Application E2E deliberately shares one real Store across UI, MCP and actor writers.
func TestFactMemoryApplicationBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		for _, width := range []int{1440, 390} {
			t.Run(fmt.Sprintf("%s/%d", base, width), func(t *testing.T) {
				runFactMemoryApplicationBrowser(t, base, width)
			})
		}
	}
}

func runFactMemoryApplicationBrowser(t *testing.T, base string, width int) {
	t.Helper()
	provider, config := newHTTPAppTestState(t)
	config.Server.BasePath = base
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		createAccessTestUser(t, provider.Users(), usercmd.User{ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	}
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, memory.MemoryFileName), []byte(browserImportedFact), 0o600); err != nil {
		t.Fatal(err)
	}
	store := memory.NewStore(provider.AppKV(), legacy, true)
	operations := memorybackofficeapp.New(memory.NewManagement(store, provider.MemoryManagement()))
	modes := &browserFactModes{FactMemoryOperations: operations, disabled: memorybackofficeapp.New(memory.NewManagement(memory.NewStore(provider.AppKV(), legacy, false), provider.MemoryManagement()))}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "fact-browser", Version: "1"}, nil)
	memory.RegisterTools(mcpServer, store)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = mcpServer.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "fact-browser-client", Version: "1"}, nil)
	mcpSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpSession.Close() }()
	actor := actors.NewMemoryActorExecutor(store, nil)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	config.Server.PublicURL = "http://" + server.Listener.Addr().String()
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	app.factMemory = modes
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/__fact_test/") {
			handler.ServeHTTP(w, r)
			return
		}
		var operationErr error
		switch strings.TrimPrefix(r.URL.Path, "/__fact_test/") {
		case "snapshot":
			var value any
			var ok bool
			value, ok, operationErr = provider.AppKV().GetJSON(r.Context(), memorycmd.Key)
			if operationErr == nil && ok {
				w.Header().Set("Content-Type", "application/json")
				operationErr = json.NewEncoder(w).Encode(value)
			}
		case "mcp":
			var result *mcp.CallToolResult
			result, operationErr = mcpSession.CallTool(r.Context(), &mcp.CallToolParams{Name: "balda.memory.remember", Arguments: map[string]any{"fact": "Synthetic MCP accepted fact"}})
			if operationErr == nil && result.IsError {
				operationErr = fmt.Errorf("MCP remember rejected")
			}
		case "actor":
			var envelopeErr error
			env, envelopeErr := actors.MemoryRememberEnvelope(actors.MemoryRememberPayload{Fact: "Synthetic actor accepted fact", SourceSessionID: "synthetic-browser-session"})
			operationErr = envelopeErr
			if operationErr == nil {
				operationErr = actor.Handle(r.Context(), env)
			}
		case "unavailable":
			modes.mode.Store(1)
		case "disable-memory":
			modes.mode.Store(2)
		case "enabled":
			modes.mode.Store(0)
		case "revoke":
			at := time.Now().UTC()
			operationErr = provider.Users().RevokeUserSessions(r.Context(), "administrator", at, "synthetic-test", usercmd.AuditEvent{ID: "browser-revoke", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: "administrator", Source: "test", OccurredAt: at})
		default:
			http.NotFound(w, r)
			return
		}
		if operationErr != nil {
			http.Error(w, "synthetic fixture operation failed", http.StatusInternalServerError)
		}
	})
	server.Start()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/fact-memory.cjs", server.URL, base, fmt.Sprint(width))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated fact browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	value, ok, err := provider.AppKV().GetJSON(t.Context(), memorycmd.Key)
	if err != nil || !ok {
		t.Fatalf("durable record exists=%v error=%v", ok, err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var record memorycmd.Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != 9 || record.UpdatedAt == "" || len(record.Entries) != 0 {
		t.Fatalf("final record version=%d initialized=%v entries=%d", record.Version, record.UpdatedAt != "", len(record.Entries))
	}
	reopened := memory.NewStore(provider.AppKV(), legacy, true)
	snapshot, err := reopened.Snapshot(t.Context())
	if err != nil || snapshot.Content != "" || snapshot.Version != 9 || snapshot.UpdatedAt == "" {
		t.Fatalf("empty record retained after reopening: version=%d initialized=%v error=%v", snapshot.Version, snapshot.UpdatedAt != "", err)
	}
	audits, err := provider.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range audits.Events {
		if event.TargetType != usercmd.AuditTargetMemoryFact {
			continue
		}
		count++
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"Synthetic", "browser draft", "saved replacement", "denied fact"} {
			if strings.Contains(string(encoded), body) {
				t.Fatal("fact body leaked into audit")
			}
		}
	}
	if count != 6 {
		t.Fatalf("successful UI mutation audits=%d, want6", count)
	}
}

// Modes inject deterministic adapter failures without changing production wiring.
type browserFactModes struct {
	FactMemoryOperations
	mode     atomic.Int32
	disabled FactMemoryOperations
}

func (m *browserFactModes) Inventory(ctx context.Context, query string, authority memorycmd.Authority) (memorycmd.Inventory, error) {
	if m.mode.Load() == 2 {
		return m.disabled.Inventory(ctx, query, authority)
	}
	return m.FactMemoryOperations.Inventory(ctx, query, authority)
}

func (m *browserFactModes) Create(ctx context.Context, request memorycmd.Create) (memorycmd.Fact, error) {
	switch m.mode.Load() {
	case 1:
		return memorycmd.Fact{}, memorycmd.ErrUnavailable
	case 2:
		return m.disabled.Create(ctx, request)
	default:
		return m.FactMemoryOperations.Create(ctx, request)
	}
}
