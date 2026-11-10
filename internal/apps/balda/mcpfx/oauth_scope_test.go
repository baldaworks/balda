package mcpfx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

func TestWorkerProtocolRequestsPreserveResourceAndRenewalScopes(t *testing.T) {
	for _, flow := range []string{"browser", "device"} {
		t.Run(flow, func(t *testing.T) {
			var origin string
			const scope = "openid offline_access"
			const redirect = "https://backoffice.example.org/mcp/oauth/callback"
			requests := make(map[string]int)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/register" {
					var body struct {
						Scope        string   `json:"scope"`
						RedirectURIs []string `json:"redirect_uris"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Scope != scope || len(body.RedirectURIs) != 1 || body.RedirectURIs[0] != redirect {
						t.Error("registration lost selected resource/renewal scope or exact redirect")
					}
					requests["registration"]++
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "scope-client", "token_endpoint_auth_method": "none", "redirect_uris": []string{redirect}})
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("scope") != scope || r.Form.Get("resource") != origin+"/mcp" || r.Form.Get("client_id") != "scope-client" {
					t.Error("protocol request lost selected scopes/resource/client")
				}
				if r.URL.Path == "/device" {
					requests["device"]++
					_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "private-device", "user_code": "CODE", "verification_uri": origin + "/verify", "expires_in": 60, "interval": 1})
					return
				}
				if r.URL.Path != "/token" {
					http.NotFound(w, r)
					return
				}
				requests[r.Form.Get("grant_type")]++
				// Omitted scope retains request semantics at the policy boundary.
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "scope-access", "refresh_token": "scope-refresh", "token_type": "Bearer", "expires_in": 3600})
			}))
			defer server.Close()
			origin = server.URL
			provider := NewOAuthProvider(server.Client())
			metadata := mcpmanage.OAuthMetadata{Resource: origin + "/mcp", Issuer: origin, AuthorizationEndpoint: origin + "/authorize", TokenEndpoint: origin + "/token", DeviceAuthorizationEndpoint: origin + "/device", RegistrationEndpoint: origin + "/register", ResourceScopes: []string{"openid"}, Scopes: []string{"openid", "profile", "email", "offline_access"}, AuthMethods: []string{mcpcmd.ClientAuthNone}, PKCEMethods: []string{workerPKCEMethod}, GrantTypes: []string{"authorization_code", "refresh_token", deviceGrantType}}
			selected := strings.Fields(scope)
			client, err := provider.Register(t.Context(), metadata, redirect, selected)
			if err != nil {
				t.Fatal(err)
			}
			grant := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: client.ID}, TokenEndpointAuthMethod: client.AuthMethod, Scopes: selected}
			var token mcpmanage.OAuthToken
			if flow == "browser" {
				location, verifier, beginErr := provider.BeginCode(metadata, grant, redirect, "state")
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				u, parseErr := url.Parse(location)
				if parseErr != nil || u.Query().Get("scope") != scope {
					t.Fatal("authorization URL lost selected scope")
				}
				token, err = provider.ExchangeCode(t.Context(), metadata, grant, mcpmanage.GrantSecrets{}, redirect, "code", verifier)
			} else {
				device, beginErr := provider.BeginDevice(t.Context(), metadata, grant, mcpmanage.GrantSecrets{})
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				token, err = provider.PollDevice(t.Context(), metadata, grant, mcpmanage.GrantSecrets{}, device)
			}
			if err != nil || token.Scopes != nil || token.Secrets.RefreshToken != "scope-refresh" {
				t.Fatalf("scope omission/renewal credential lost: %v", err)
			}
			if _, err := provider.Refresh(t.Context(), metadata, grant, token.Secrets); err != nil {
				t.Fatal(err)
			}
			if requests["registration"] != 1 || requests["refresh_token"] != 1 || (flow == "browser" && requests["authorization_code"] != 1) || (flow == "device" && (requests["device"] != 1 || requests[deviceGrantType] != 1)) {
				t.Fatalf("protocol stages skipped or repeated: %v", requests)
			}
		})
	}
}
