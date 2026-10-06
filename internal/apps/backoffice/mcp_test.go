package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestMCPRoutesRequireCurrentAdministrator(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
		createAccessTestUser(t, provider.Users(), usercmd.User{
			ID: string(role), Username: string(role), NormalizedUsername: string(role), DisplayName: string(role), Role: role,
			Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator,
			Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, string(usercmd.RoleAdministrator))
	operator := loginHTTPAppSession(t, handler, config, string(usercmd.RoleOperator))
	for _, route := range []string{"/mcp", "/mcp/new", "/mcp/connections/fixture"} {
		for _, tc := range []struct {
			name, access string
			status       int
		}{
			{"anonymous", "", http.StatusUnauthorized},
			{"operator", operator.access, http.StatusForbidden},
			{"administrator without management port", admin.access, http.StatusServiceUnavailable},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, route, nil)
				if tc.access != "" {
					request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: tc.access})
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if tc.access == admin.access && !strings.Contains(response.Body.String(), `class="app-header`) {
					t.Fatal("administrator unavailable page lost shared shell")
				}
				if response.Code != tc.status {
					t.Fatalf("GET %s = %d, want %d", route, response.Code, tc.status)
				}
				if strings.Count(response.Body.String(), `id="main-content"`) != 1 || response.Header().Get("Cache-Control") != noStoreCacheControl {
					t.Fatal("protected MCP response lost shared rendering or cache policy")
				}
			})
		}
	}
	fixture := &mcpHTTPFixture{items: []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "fixture", Source: mcpcmd.SourceManaged, Version: 1}}}}
	app.mcp = fixture
	for _, route := range []string{"/mcp/connections", "/mcp/connections/fixture", "/mcp/connections/fixture/selection", "/mcp/connections/fixture/delete"} {
		for _, tc := range []struct {
			access, csrf string
			status       int
		}{{"", "anonymous", 401}, {operator.access, operator.csrf, 403}} {
			w := performAccessMutation(t, handler, config, route, url.Values{"csrf_token": {tc.csrf}}, tc.access, tc.csrf, false)
			if w.Code != tc.status || len(fixture.creates)+len(fixture.updates)+len(fixture.selections)+fixture.probes != 0 {
				t.Fatal("unauthorized mutation reached MCP owner")
			}
		}
	}
}

func TestMCPInventoryUsesSafeSharedRendering(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "administrator", Username: "administrator", NormalizedUsername: "administrator", DisplayName: "Administrator", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	app.mcp = &mcpHTTPFixture{items: []mcpcmd.Item{
		{Connection: mcpcmd.Connection{ID: "managed-one", PublicID: "worker-tools", Source: mcpcmd.SourceManaged, Enabled: true, Version: 2}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", OAuth: true, Targets: mcpcmd.Targets{Providers: []string{"alpha"}}, Headers: map[string]mcpcmd.ValueBinding{"X-Worker": {Kind: mcpcmd.ValueProtected}}}, Status: mcpcmd.StatusUnavailable, Authorization: mcpcmd.GrantAuthorized},
		{Connection: mcpcmd.Connection{ID: "config:protected", PublicID: "protected", Source: mcpcmd.SourceConfig, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportSSE, URL: "https://protected.example/sse", Targets: mcpcmd.Targets{All: true}}, Status: mcpcmd.StatusPending, Recovery: mcpcmd.RecoveryFirstAuthorization},
	}}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	login := loginHTTPAppSession(t, handler, config, "administrator")
	for _, shape := range []string{"full", "fragment", "history"} {
		t.Run(shape, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: login.access})
			if shape != "full" {
				request.Header.Set("HX-Request", "true")
				request.Header.Set("HX-Target", "main-content")
			}
			if shape == "history" {
				request.Header.Set("HX-History-Restore-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusOK {
				t.Fatalf("inventory = %d", response.Code)
			}
			for _, visible := range []string{"worker-tools", "protected", "alpha", "Authorized", "Tools unavailable", "This server requires authorization", "Configuration"} {
				if !strings.Contains(body, visible) {
					t.Errorf("missing visible %q", visible)
				}
			}
			if strings.Count(body, `id="main-content"`) != 1 || response.Header().Get("Cache-Control") != noStoreCacheControl {
				t.Fatal("inventory lost shared rendering/cache policy")
			}
			if got := strings.Contains(strings.ToLower(body), "<!doctype html>"); got != (shape != "fragment") {
				t.Fatalf("document=%t for %s", got, shape)
			}
			if strings.Contains(body, "Available</span>") {
				t.Fatal("authorized grant was presented as runtime ready")
			}
		})
	}
}

type mcpHTTPFixture struct {
	inventoryErr error
	items        []mcpcmd.Item
	err          error
	creates      []mcpcmd.CreateDefinition
	updates      []mcpcmd.UpdateDefinition
	selections   []mcpcmd.ChangeSelection
	probes       int
}

func (f *mcpHTTPFixture) result() (mcpcmd.Item, error) {
	if f.err != nil {
		return mcpcmd.Item{}, f.err
	}
	return f.items[0], nil
}

func (f *mcpHTTPFixture) Inventory(context.Context) ([]mcpcmd.Item, error) {
	return f.items, f.inventoryErr
}
func (*mcpHTTPFixture) ProviderIDs(context.Context) ([]string, error) {
	return []string{"alpha", "beta"}, nil
}
func (f *mcpHTTPFixture) Create(_ context.Context, r mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	f.creates = append(f.creates, r)
	return f.result()
}
func (f *mcpHTTPFixture) Update(_ context.Context, r mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	f.updates = append(f.updates, r)
	return f.result()
}
func (f *mcpHTTPFixture) SetEnabled(_ context.Context, r mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	f.selections = append(f.selections, r)
	return f.result()
}
func (f *mcpHTTPFixture) Delete(_ context.Context, r mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	f.selections = append(f.selections, r)
	return f.result()
}
func (f *mcpHTTPFixture) Probe(context.Context, mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	f.probes++
	return f.result()
}
func (f *mcpHTTPFixture) ProbeUpdate(context.Context, mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	f.probes++
	return f.result()
}

func TestMCPFormsFenceAuthorityAndNeverReflectValues(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &mcpHTTPFixture{items: []mcpcmd.Item{
		{Connection: mcpcmd.Connection{ID: "managed", PublicID: "worker", Source: mcpcmd.SourceManaged, Version: 2, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", Targets: mcpcmd.Targets{All: true}, Headers: map[string]mcpcmd.ValueBinding{"X-Secret": {Kind: mcpcmd.ValueProtected}}}, Status: mcpcmd.StatusPending},
		{Connection: mcpcmd.Connection{ID: "config:worker", PublicID: "configured", Source: mcpcmd.SourceConfig}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "worker", Targets: mcpcmd.Targets{All: true}}, Status: mcpcmd.StatusReady},
	}}
	app.mcp = fixture
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	login := loginHTTPAppSession(t, handler, config, "admin")
	for _, route := range []string{"/mcp/new", "/mcp/connections/managed", "/mcp/connections/config:worker"} {
		r := httptest.NewRequest(http.MethodGet, route, nil)
		r.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: login.access})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("editor %s: %d", route, w.Code)
		}
		if strings.Contains(route, "config:") {
			if strings.Contains(w.Body.String(), `name="expected_version"`) {
				t.Fatal("configured entry has mutation form")
			}
		} else if !strings.Contains(w.Body.String(), `name="transport"`) {
			t.Fatal("editor missing transport form")
		}
	}
	for _, shape := range []string{"full", "fragment", "history"} {
		for _, tc := range []struct {
			path    string
			failure error
			status  int
		}{{"/mcp/connections/missing", nil, 404}, {"/mcp", mcpcmd.ErrUnavailable, 503}} {
			fixture.inventoryErr = tc.failure
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.AddCookie(&http.Cookie{Name: security.AccessCookieName, Value: login.access})
			if shape != "full" {
				request.Header.Set("HX-Request", "true")
				request.Header.Set("HX-Target", "main-content")
			}
			if shape == "history" {
				request.Header.Set("HX-History-Restore-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != tc.status || strings.Count(body, `id="main-content"`) != 1 {
				t.Fatal("GET error lost status or shared main")
			}
			if shape != "fragment" && !strings.Contains(body, `class="app-header`) {
				t.Fatal("authenticated error lost shared shell")
			}
		}
	}
	fixture.inventoryErr = nil
	form := url.Values{"csrf_token": {login.csrf}, "public_id": {"worker"}, "transport": {"http"}, "url": {"https://worker.example/mcp"}, "targets_all": {"yes"}, "header_key": {"X-Secret"}, "header_operation": {"set"}, "header_kind": {"protected"}, "header_value": {"write-only-canary"}}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = append([]string(nil), v...)
	}
	bad.Set("csrf_token", "invalid")
	if w := performAccessMutation(t, handler, config, "/mcp/connections", bad, login.access, login.csrf, true); w.Code != 403 || len(fixture.creates) != 0 {
		t.Fatal("CSRF request reached MCP owner")
	}
	for _, htmx := range []bool{false, true} {
		w := performAccessMutation(t, handler, config, "/mcp/connections", form, login.access, login.csrf, htmx)
		want := 303
		if htmx {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("create: %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "write-only-canary") || strings.Contains(w.Header().Get("Location"), "canary") || strings.Contains(w.Header().Get("HX-Location"), "canary") {
			t.Fatal("write-only input leaked")
		}
	}
	request := fixture.creates[0]
	if !request.Enabled {
		t.Error("new connection was not enabled for its selected providers")
	}
	if request.Values.Headers["X-Secret"].Value != "write-only-canary" || request.Authority.UserID != "admin" || request.Authority.SessionID == "" || request.Authority.SessionVersion == 0 {
		t.Fatal("write or current authority lost")
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{mcpcmd.ErrInvalid, 400}, {mcpcmd.ErrForbidden, 403}, {mcpcmd.ErrNotFound, 404}, {mcpcmd.ErrConflict, 409}, {errors.New("private SDK payload write-only-canary"), 503}} {
		fixture.err = tc.err
		w := performAccessMutation(t, handler, config, "/mcp/connections", form, login.access, login.csrf, true)
		if w.Code != tc.status || strings.Count(w.Body.String(), `id="main-content"`) != 1 || strings.Contains(w.Body.String(), "write-only-canary") {
			t.Fatalf("safe error mapping: %d, want %d", w.Code, tc.status)
		}
	}
	fixture.err = nil
	form.Set("expected_version", "2")
	w := performAccessMutation(t, handler, config, "/mcp/connections/managed", form, login.access, login.csrf, true)
	if w.Code != 204 || len(fixture.updates) != 1 || fixture.updates[0].ExpectedVersion != 2 || !fixture.updates[0].Enabled {
		t.Fatal("versioned update failed")
	}
	w = performAccessMutation(t, handler, config, "/mcp/connections/config:worker", form, login.access, login.csrf, false)
	if w.Code != 403 || len(fixture.updates) != 1 {
		t.Fatal("configured mutation reached owner")
	}
	form.Set("operation", "probe")
	w = performAccessMutation(t, handler, config, "/mcp/connections/managed", form, login.access, login.csrf, true)
	if !strings.Contains(w.Body.String(), `name="csrf_token" value="`+login.csrf+`"`) {
		t.Fatal("probe lost current CSRF")
	}
	if w.Code != 200 || fixture.probes != 1 || len(fixture.updates) != 1 || strings.Contains(w.Body.String(), "write-only-canary") {
		t.Fatal("candidate probe persisted or leaked values")
	}
	form.Del("operation")
	for _, action := range []string{"selection", "delete"} {
		w = performAccessMutation(t, handler, config, "/mcp/connections/managed/"+action, form, login.access, login.csrf, true)
		if w.Code != 400 {
			t.Fatal("missing confirmation accepted")
		}
		form.Set("confirm", "yes")
		w = performAccessMutation(t, handler, config, "/mcp/connections/managed/"+action, form, login.access, login.csrf, true)
		if w.Code != 204 {
			t.Fatalf("confirmed %s: %d", action, w.Code)
		}
		form.Del("confirm")
	}
	// A supported128-binding revision renders two unused editor slots. Native
	// encoding exceeds the small-account-form limit but remains bounded for MCP.
	large := url.Values{"csrf_token": {login.csrf}, "expected_version": {"2"}, "transport": {"http"}, "url": {"https://worker.example/mcp"}, "enabled": {"yes"}, "targets_all": {"yes"}}
	for index := range 130 {
		key := ""
		if index < 128 {
			key = fmt.Sprintf("X-Value-%03d", index)
		}
		large.Add("header_key", key)
		large.Add("header_kind", "protected")
		large.Add("header_operation", "keep")
		large.Add("header_value", "")
	}
	before := len(fixture.updates)
	w = performAccessMutation(t, handler, config, "/mcp/connections/managed", large, login.access, login.csrf, false)
	if w.Code != 303 || len(fixture.updates) != before+1 || len(fixture.updates[before].Values.Headers) != 128 {
		t.Fatal("supported binding editor rejected unused slots or native body")
	}
	large.Set("operation", "probe")
	w = performAccessMutation(t, handler, config, "/mcp/connections/managed", large, login.access, login.csrf, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `title="admin">admin</strong>`) || !strings.Contains(w.Body.String(), `name="csrf_token" value="`+login.csrf+`"`) {
		t.Fatal("supported native binding probe lost current authority or was rejected")
	}
	large.Set("padding", strings.Repeat("a", (1<<20)+1))
	before = fixture.probes
	w = performAccessMutation(t, handler, config, "/mcp/connections/managed", large, login.access, login.csrf, false)
	if w.Code != 400 || fixture.probes != before {
		t.Fatal("oversized body reached owner")
	}
	fixture.items[0].Connection.Enabled = false
	form.Del("operation")
	w = performAccessMutation(t, handler, config, "/mcp/connections/managed", form, login.access, login.csrf, false)
	if w.Code != 303 || fixture.updates[len(fixture.updates)-1].Enabled {
		t.Fatal("saving a disabled definition enabled the connection")
	}
}

func TestMCPIntakePreservesTransportFieldsForOwnerValidation(t *testing.T) {
	definition, _, err := parseMCPDefinition(url.Values{"transport": {"http"}, "url": {"https://worker.example/mcp"}, "command": {"worker"}, "args": {"--fixed\ntwo words"}, "directory": {"/worker"}})
	if err != nil || definition.Command != "worker" || len(definition.Args) != 2 || definition.Args[1] != "two words" || definition.Directory != "/worker" || definition.URL != "https://worker.example/mcp" {
		t.Fatal("intake silently discarded fields before owner validation")
	}
}

type committedMCPCreatePort struct{ *mcpHTTPFixture }

func (f *committedMCPCreatePort) Create(_ context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	f.creates = append(f.creates, request)
	saved := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "saved-worker", PublicID: request.PublicID, Source: mcpcmd.SourceManaged, Version: 1, Enabled: true}, Definition: request.Definition, Status: mcpcmd.StatusPending}
	f.items = append(f.items, saved)
	return saved, mcpcmd.ErrUnavailable
}
func TestCommittedMCPCreateHasRecoveryLink(t *testing.T) {
	for _, base := range []string{"", "/balda"} {
		t.Run("base="+base, func(t *testing.T) { testCommittedMCPCreateHasRecoveryLink(t, base) })
	}
}

func testCommittedMCPCreateHasRecoveryLink(t *testing.T, base string) {
	provider, config := newHTTPAppTestState(t)
	config.Server.BasePath = base
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	port := &committedMCPCreatePort{mcpHTTPFixture: &mcpHTTPFixture{}}
	app.mcp = port
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	login := loginHTTPAppSession(t, handler, config, "admin")
	for _, fragment := range []bool{false, true} {
		t.Run(fmt.Sprint(fragment), func(t *testing.T) {
			response := performAccessMutation(t, handler, config, base+"/mcp/connections", url.Values{"csrf_token": {login.csrf}, "public_id": {"worker"}, "transport": {"http"}, "url": {"https://worker.example/mcp"}, "targets_all": {"true"}, "operation": {"save"}, "header_key": {"X-Private"}, "header_operation": {"set"}, "header_kind": {"protected"}, "header_value": {"private-recovery-marker"}}, login.access, login.csrf, fragment)
			body := response.Body.String()
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(body, `href="`+base+`/mcp/connections/saved-worker"`) {
				t.Errorf("post-commit status/link = %d/%t", response.Code, strings.Contains(body, `href="`+base+`/mcp/connections/saved-worker"`))
			}
			if !strings.Contains(body, "saved") || strings.Contains(body, "private-recovery-marker") || response.Header().Get("Cache-Control") != noStoreCacheControl {
				t.Error("saved response lost its explicit outcome, input clearing or no-store policy")
			}
		})
	}
	for _, operation := range []string{"save", "probe"} {
		t.Run(operation+" without commit", func(t *testing.T) {
			app.mcp = &mcpHTTPFixture{err: mcpcmd.ErrUnavailable}
			response := performAccessMutation(t, handler, config, base+"/mcp/connections", url.Values{"csrf_token": {login.csrf}, "public_id": {"worker"}, "transport": {"http"}, "url": {"https://worker.example/mcp"}, "targets_all": {"true"}, "operation": {operation}}, login.access, login.csrf, false)
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "Open saved connection") {
				t.Fatal("pre-commit failure or candidate probe advertised a saved connection")
			}
		})
	}
}
