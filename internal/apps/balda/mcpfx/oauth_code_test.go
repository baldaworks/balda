package mcpfx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"golang.org/x/oauth2"
)

func TestWorkerBrowserCodeUsesS256AndExactResourceClientRedirect(t *testing.T) {
	var base, verifier string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != "browser-client" || r.Form.Get("code") != "native-code" || r.Form.Get("code_verifier") != verifier || r.Form.Get("redirect_uri") != "https://backoffice.example.org/balda/mcp/oauth/callback" || r.Form.Get("resource") != base+"/mcp" || r.Form.Get("scope") != "tools" {
			t.Error("code exchange lost redirect/PKCE/worker resource binding")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "browser-access", "refresh_token": "browser-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "tools"})
	}))
	defer issuer.Close()
	base = issuer.URL
	p := NewOAuthProvider(issuer.Client())
	metadata := mcpmanage.OAuthMetadata{RequireIssuerParameter: true, Resource: base + "/mcp", Issuer: base, AuthorizationEndpoint: base + "/authorize", TokenEndpoint: base + "/token", PKCEMethods: []string{"S256"}, GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "browser-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone, Scopes: []string{"tools"}}
	location, v, err := p.BeginCode(metadata, g, "https://backoffice.example.org/balda/mcp/oauth/callback", "one-use-state")
	if err != nil {
		t.Fatalf("native authorization request failed: %v", err)
	}
	verifier = v
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != "one-use-state" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(verifier) || q.Get("resource") != g.Binding.Resource || q.Get("client_id") != g.Binding.ClientID {
		t.Fatal("native redirect lost state/PKCE/worker binding")
	}
	token, err := p.ExchangeCode(t.Context(), metadata, g, mcpmanage.GrantSecrets{}, "https://backoffice.example.org/balda/mcp/oauth/callback", "native-code", verifier)
	if err != nil || token.Secrets.AccessToken != "browser-access" || token.Secrets.RefreshToken != "browser-refresh" {
		t.Fatalf("worker code exchange failed: %v", err)
	}
}

func TestWorkerCallbackUsesExactBackofficeOriginAndBasePath(t *testing.T) {
	for _, tc := range []struct{ origin, base, want string }{
		{"https://backoffice.example.org", "", "https://backoffice.example.org/mcp/oauth/callback"},
		{"https://backoffice.example.org/", "/balda", "https://backoffice.example.org/balda/mcp/oauth/callback"},
		{"http://127.0.0.1:8095", "/balda", "http://127.0.0.1:8095/balda/mcp/oauth/callback"},
	} {
		got, err := MCPCallbackURL(tc.origin, tc.base)
		if err != nil || got != tc.want {
			t.Fatalf("callback=%s, want %s, err=%v", got, tc.want, err)
		}
	}
	for _, tc := range []struct{ origin, base string }{
		{"https://backoffice.example.org", "//foreign.example.org"},
		{"https://backoffice.example.org", "/balda/../other"},
		{"https://backoffice.example.org/wrong-origin-path", ""},
		{"https://backoffice.example.org?redirect=foreign", ""},
		{"http://non-loopback.example.org", ""},
	} {
		if _, err := MCPCallbackURL(tc.origin, tc.base); err == nil {
			t.Fatal("invalid callback origin/base accepted")
		}
	}
}
