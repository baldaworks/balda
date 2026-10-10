package balda

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/normahq/runtime/v2/appconfig"
)

const testHTTPListenAddr = "127.0.0.1:18095"

func TestResolvedHTTPURLs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		basePath string
		want     []string
	}{
		{
			name: "asus prefix", basePath: "/balda",
			want: []string{
				"https://lab.metalagman.dev/balda/backoffice/",
				"https://lab.metalagman.dev/balda/backoffice/webhooks",
				"https://lab.metalagman.dev/balda/webhooks/orders",
				"https://lab.metalagman.dev/balda/gateway/slack/events",
				"https://lab.metalagman.dev/balda/gateway/slack/commands",
			},
		},
		{
			name: "root prefix", basePath: "",
			want: []string{
				"https://lab.metalagman.dev/backoffice/",
				"https://lab.metalagman.dev/backoffice/webhooks",
				"https://lab.metalagman.dev/webhooks/orders",
				"https://lab.metalagman.dev/gateway/slack/events",
				"https://lab.metalagman.dev/gateway/slack/commands",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := BaldaConfig{HTTP: HTTPConfig{
				ListenAddr: testHTTPListenAddr, BaseURL: "https://lab.metalagman.dev", BasePath: &tt.basePath,
			}}
			resolved, err := cfg.ResolveHTTP()
			if err != nil {
				t.Fatal(err)
			}
			if got := resolved.ListenAddr; got != testHTTPListenAddr {
				t.Fatalf("listen address = %q, want independent local bind", got)
			}
			got := []string{
				resolved.PublicURL(resolved.BrowserPath() + "/"),
				resolved.PublicURL(resolved.BrowserPath() + "/webhooks"),
				resolved.PublicURL(resolved.ManagedWebhookPath("orders")),
				resolved.PublicURL(resolved.GatewayPath("slack", "events")),
				resolved.PublicURL(resolved.GatewayPath("slack", "commands")),
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("URL %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestResolveHTTPFallsBackToBackofficeSettings(t *testing.T) {
	cfg := BaldaConfig{Backoffice: backoffice.ServerConfig{
		ListenAddr: "127.0.0.1:19095", PublicURL: "https://old.example.test/", BasePath: "/old",
	}}
	resolved, err := cfg.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ListenAddr != "127.0.0.1:19095" || resolved.BaseURL != "https://old.example.test" || resolved.BasePath != "/old" {
		t.Fatalf("legacy fallback = %+v", resolved)
	}

	empty := ""
	cfg.HTTP = HTTPConfig{ListenAddr: testHTTPListenAddr, BaseURL: "https://new.example.test", BasePath: &empty}
	resolved, err = cfg.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ListenAddr != testHTTPListenAddr || resolved.BaseURL != "https://new.example.test" || resolved.BasePath != "" {
		t.Fatalf("shared override = %+v", resolved)
	}
}

func TestResolveHTTPAcceptsExplicitEmptyBasePathFromConfig(t *testing.T) {
	var decoded struct {
		Balda BaldaConfig `mapstructure:"balda"`
	}
	settings := map[string]any{"balda": map[string]any{
		"backoffice": map[string]any{"base_path": "/legacy"},
		"http":       map[string]any{"base_path": ""},
	}}
	if err := appconfig.DecodeSettings(settings, &decoded); err != nil {
		t.Fatal(err)
	}
	resolved, err := decoded.Balda.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BasePath != "" {
		t.Fatalf("base path = %q, want explicit root override", resolved.BasePath)
	}
}

func TestResolveSharedBackofficeServerUsesSharedBrowserPath(t *testing.T) {
	basePath := "/balda"
	cfg := BaldaConfig{HTTP: HTTPConfig{
		ListenAddr: testHTTPListenAddr, BaseURL: "https://lab.metalagman.dev", BasePath: &basePath,
	}}
	server, err := cfg.ResolveSharedBackofficeServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.ListenAddr != testHTTPListenAddr || server.PublicURL != "https://lab.metalagman.dev" || server.BasePath != "/balda/backoffice" {
		t.Fatalf("Backoffice server = %+v", server)
	}
	callback, err := mcpfx.MCPCallbackURL(server.PublicURL, server.BasePath)
	if err != nil {
		t.Fatal(err)
	}
	if callback != "https://lab.metalagman.dev/balda/backoffice/mcp/oauth/callback" {
		t.Errorf("MCP OAuth callback = %q", callback)
	}
}

func TestResolveBackofficeServerKeepsLegacyRouteAndSocketUntilCutover(t *testing.T) {
	basePath := "/balda"
	cfg := BaldaConfig{
		Backoffice: backoffice.ServerConfig{ListenAddr: "127.0.0.1:19095", PublicURL: "https://old.example.test", BasePath: "/old"},
		HTTP:       HTTPConfig{ListenAddr: testHTTPListenAddr, BaseURL: "https://lab.metalagman.dev", BasePath: &basePath},
	}
	server, err := cfg.ResolveBackofficeServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.ListenAddr != "127.0.0.1:19095" || server.PublicURL != "https://old.example.test" || server.BasePath != "/old" {
		t.Fatalf("legacy Backoffice server = %+v", server)
	}
}

func TestResolveHTTPRejectsInvalidFields(t *testing.T) {
	for _, tt := range []struct {
		name  string
		cfg   HTTPConfig
		field string
	}{
		{name: "origin path", cfg: HTTPConfig{BaseURL: "https://lab.metalagman.dev/extra"}, field: "balda.http.base_url"},
		{name: "origin trailing slash", cfg: HTTPConfig{BaseURL: "https://lab.metalagman.dev/"}, field: "balda.http.base_url"},
		{name: "origin credentials", cfg: HTTPConfig{BaseURL: "https://user:secret@lab.metalagman.dev"}, field: "balda.http.base_url"},
		{name: "empty query marker", cfg: HTTPConfig{BaseURL: "https://lab.metalagman.dev?"}, field: "balda.http.base_url"},
		{name: "empty fragment marker", cfg: HTTPConfig{BaseURL: "https://lab.metalagman.dev#"}, field: "balda.http.base_url"},
		{name: "relative base path", cfg: HTTPConfig{BasePath: stringPtr("balda")}, field: "balda.http.base_path"},
		{name: "trailing base path slash", cfg: HTTPConfig{BasePath: stringPtr("/balda/")}, field: "balda.http.base_path"},
		{name: "invalid bind port", cfg: HTTPConfig{ListenAddr: "127.0.0.1:not-a-port"}, field: "balda.http.listen_addr"},
		{name: "public HTTP bind", cfg: HTTPConfig{ListenAddr: "0.0.0.0:8095", BaseURL: "http://localhost:8095"}, field: "balda.http.base_url"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (BaldaConfig{HTTP: tt.cfg}).ResolveHTTP()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ResolveHTTP() error = %v, want %s", err, tt.field)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }
