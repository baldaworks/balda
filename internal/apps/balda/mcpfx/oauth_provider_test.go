package mcpfx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

const workerOAuthScope = "tools"
const workerAuthorizationMetadataPath = "/.well-known/oauth-authorization-server"

func TestWorkerOAuthSDKDiscoveryRegistrationAndBoundRotation(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{workerOAuthScope}, "bearer_methods_supported": []string{"header"}})
		case workerAuthorizationMetadataPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{mcpcmd.ClientAuthSecretBasic}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "scopes_supported": []string{workerOAuthScope}})
		case "/register":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["scope"] != workerOAuthScope || request["token_endpoint_auth_method"] != mcpcmd.ClientAuthSecretBasic {
				t.Error("registration did not restrict worker capabilities")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "installation-client", "client_secret": "registration-secret", "client_secret_expires_at": 0, "token_endpoint_auth_method": mcpcmd.ClientAuthSecretBasic, "redirect_uris": []string{base + "/callback"}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != "installation-client" || secret != "registration-secret" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("resource") != base+"/mcp" || r.Form.Get("scope") != workerOAuthScope {
				t.Error("refresh crossed worker resource/client/scope binding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-access", "refresh_token": "fresh-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": workerOAuthScope, "resource": base + "/mcp"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base = server.URL
	p := NewOAuthProvider(server.Client())
	metadata, err := p.Discover(t.Context(), base+"/mcp", "")
	if err != nil || metadata.Resource != base+"/mcp" || metadata.Issuer != base {
		t.Fatalf("supported metadata unavailable: %v", err)
	}
	client, err := p.Register(t.Context(), metadata, base+"/callback", []string{workerOAuthScope})
	if err != nil || client.ID != "installation-client" || client.Secret != "registration-secret" {
		t.Fatalf("worker registration unavailable: %v", err)
	}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: client.ID}, TokenEndpointAuthMethod: client.AuthMethod, Scopes: []string{workerOAuthScope}}
	token, err := p.Refresh(t.Context(), metadata, g, mcpmanage.GrantSecrets{RefreshToken: "old-refresh", ClientSecret: client.Secret})
	if err != nil || token.Secrets.AccessToken != "fresh-access" || token.Secrets.RefreshToken != "fresh-refresh" || !token.ExpiresAt.After(time.Now()) || strings.Join(token.Scopes, " ") != workerOAuthScope {
		t.Fatalf("bound rotation unavailable: %v", err)
	}
}

func TestWorkerRegistrationBoundsIssuerResponseBeforeSDKReadsIt(t *testing.T) {
	data := `{"client_id":"bounded-client","token_endpoint_auth_method":"none","redirect_uris":["https://backoffice.example.org/callback"],"padding":"` + strings.Repeat("x", 2<<20) + `"}`
	body := &countedOAuthBody{Reader: strings.NewReader(data)}
	p := NewOAuthProvider(&http.Client{Transport: oauthFixtureTransport{body: body}})
	metadata := mcpmanage.OAuthMetadata{RegistrationEndpoint: "https://issuer.example.org/register", AuthMethods: []string{mcpcmd.ClientAuthNone}}
	if _, err := p.Register(t.Context(), metadata, "https://backoffice.example.org/callback", nil); !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatalf("unbounded registration response accepted: %v", err)
	}
	if body.read > 1<<20+1 {
		t.Fatalf("registration read %d bytes beyond bound", body.read)
	}
}

type countedOAuthBody struct {
	io.Reader
	read int
}

func (b *countedOAuthBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (*countedOAuthBody) Close() error { return nil }

type oauthFixtureTransport struct{ body io.ReadCloser }

func (t oauthFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: t.body, Request: r}, nil
}

func TestWorkerOAuthRejectsMetadataMismatchAndUnsafeSchemes(t *testing.T) {
	for _, mismatch := range []string{"resource", "issuer", "issuer trailing slash", "token endpoint", "unsupported PKCE"} {
		t.Run(mismatch, func(t *testing.T) {
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "oauth-protected-resource") {
					resource := base + "/mcp"
					if mismatch == "resource" {
						resource = base + "/other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{base}})
					return
				}
				issuer, endpoint, pkce := base, base+"/token", "S256"
				if mismatch == "issuer" {
					issuer = base + "/different-issuer"
				}
				if mismatch == "issuer trailing slash" {
					issuer = base + "/"
				}
				if mismatch == "token endpoint" {
					endpoint = "http://issuer.example.org/token"
				}
				if mismatch == "unsupported PKCE" {
					pkce = "plain"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": base + "/authorize", "token_endpoint": endpoint, "code_challenge_methods_supported": []string{pkce}})
			}))
			defer server.Close()
			base = server.URL
			if _, err := NewOAuthProvider(server.Client()).Discover(t.Context(), base+"/mcp", ""); !errors.Is(err, mcpcmd.ErrUnavailable) {
				t.Fatalf("metadata mismatch accepted or unsafe detail returned: %v", err)
			}
		})
	}
}

func TestWorkerRefreshRejectsBindingAndKeepsIssuerBodiesPrivate(t *testing.T) {
	for _, response := range []string{
		`{"error":"invalid_grant","error_description":"private-refresh-secret"}`,
		`{"access_token":"new-token","token_type":"Bearer","resource":"https://wrong.example.org/mcp"}`,
		`{"access_token":"new-token","token_type":"Bearer","iss":"https://wrong-issuer.example.org"}`,
		`{"access_token":"new-token","token_type":"Bearer","scope":["other"]}`,
		`{"access_token":"new-token","token_type":"Bearer","resource":true}`,
		`{"access_token":"new-token","token_type":"Bearer","iss":123}`,
		`{"access_token":"new-token","token_type":"Bearer","scope":null}`,
		`{"access_token":"new-token","token_type":"Bearer","resource":null}`,
		`{"access_token":"new-token","token_type":"Bearer","iss":null}`,
	} {
		t.Run(response[:20], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(response, "invalid_grant") {
					w.WriteHeader(http.StatusBadRequest)
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			p := NewOAuthProvider(server.Client())
			metadata := mcpmanage.OAuthMetadata{Resource: server.URL + "/mcp", Issuer: server.URL, TokenEndpoint: server.URL + "/token"}
			g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
			if _, err := p.Refresh(t.Context(), metadata, g, mcpmanage.GrantSecrets{RefreshToken: "old-refresh"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
				t.Fatalf("unsafe refresh result: %v", err)
			}
		})
	}
}

func TestWorkerOAuthTokenRedirectCannotForwardRegistrationCredentials(t *testing.T) {
	var leaked atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaked.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer foreign.Close()
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer issuer.Close()
	metadata := mcpmanage.OAuthMetadata{Resource: issuer.URL + "/mcp", Issuer: issuer.URL, TokenEndpoint: issuer.URL + "/token"}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthSecretBasic}
	if _, err := NewOAuthProvider(issuer.Client()).Refresh(t.Context(), metadata, g, mcpmanage.GrantSecrets{RefreshToken: "private-refresh", ClientSecret: "private-client-secret"}); !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatalf("redirect followed: %v", err)
	}
	if leaked.Load() != 0 {
		t.Fatal("foreign origin received worker token request")
	}
}

func TestWorkerRefreshUsesBoundIssuerAfterCustomResourceMetadata(t *testing.T) {
	var base string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/custom-resource-metadata":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case workerAuthorizationMetadataPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{mcpcmd.ClientAuthNone}})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "custom-prm-access", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	base = issuer.URL
	p := NewOAuthProvider(issuer.Client())
	metadata, err := p.Discover(t.Context(), base+"/mcp", base+"/custom-resource-metadata")
	if err != nil {
		t.Fatal(err)
	}
	binding := mcpcmd.AuthBinding{ConnectionID: "worker", Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "registered-client"}
	fresh, err := p.DiscoverIssuer(t.Context(), binding)
	if err != nil || fresh.Issuer != binding.Issuer {
		t.Fatalf("custom PRM grant cannot discover its bound issuer: %v", err)
	}
	grant := mcpcmd.Grant{Binding: binding, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
	token, err := p.Refresh(t.Context(), fresh, grant, mcpmanage.GrantSecrets{RefreshToken: "custom-prm-refresh"})
	if err != nil || token.Secrets.AccessToken != "custom-prm-access" {
		t.Fatalf("registered custom PRM grant cannot refresh: %v", err)
	}
}
