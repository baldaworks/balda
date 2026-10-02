package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

func TestWorkerDeviceDiscoveryAndAuthorizationWithoutBrowserCapabilities(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{workerOAuthScope}})
		case workerAuthorizationMetadataPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "token_endpoint": base + "/token", "device_authorization_endpoint": base + "/device", "grant_types_supported": []string{deviceGrantType}, "token_endpoint_auth_methods_supported": []string{mcpcmd.ClientAuthSecretBasic}})
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			id, secret, ok := r.BasicAuth()
			if !ok || id != "device-client" || secret != "client-secret" || r.Form.Get("resource") != base+"/mcp" || r.Form.Get("scope") != workerOAuthScope {
				t.Error("device request lost worker credentials/resource/scope")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "private-device-code", "user_code": "ABCD-EFGH", "verification_uri": base + "/verify", "expires_in": 60, "interval": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base = server.URL
	p := NewOAuthProvider(server.Client())
	metadata, err := p.Discover(t.Context(), base+"/mcp", "")
	if err != nil || metadata.DeviceAuthorizationEndpoint != base+"/device" {
		t.Fatalf("device-only metadata unavailable: %v", err)
	}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: base + "/mcp", Issuer: base, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthSecretBasic, Scopes: []string{workerOAuthScope}}
	device, err := p.BeginDevice(t.Context(), metadata, g, mcpmanage.GrantSecrets{ClientSecret: "client-secret"})
	if err != nil || device.Code != "private-device-code" || device.UserCode != "ABCD-EFGH" {
		t.Fatalf("native device authorization unavailable: %v", err)
	}
}

type deviceWireBody struct {
	io.Reader
	closed bool
}

func (b *deviceWireBody) Close() error { b.closed = true; return nil }

func TestWorkerDeviceStartClosesAndBoundsWireResponse(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		data := `{"device_code":"private-device-code","user_code":"ABCD-EFGH","verification_uri":"https://issuer.example.org/verify","expires_in":60,"interval":1}`
		if oversized {
			data = strings.Repeat("x", 2<<20)
		}
		body := &deviceWireBody{Reader: strings.NewReader(data)}
		p := NewOAuthProvider(&http.Client{Transport: oauthFixtureTransport{body: body}})
		metadata := mcpmanage.OAuthMetadata{Resource: "https://resource.example.org/mcp", Issuer: "https://issuer.example.org", TokenEndpoint: "https://issuer.example.org/token", DeviceAuthorizationEndpoint: "https://issuer.example.org/device", GrantTypes: []string{deviceGrantType}}
		g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
		_, err := p.BeginDevice(t.Context(), metadata, g, mcpmanage.GrantSecrets{})
		if oversized && !errors.Is(err, mcpcmd.ErrUnavailable) {
			t.Fatalf("unbounded device response accepted: %v", err)
		}
		if !oversized && err != nil {
			t.Fatal(err)
		}
		if !body.closed {
			t.Fatal("device response kept wire body open")
		}
	}
}

func TestWorkerDevicePollingRespectsPendingSlowDownAndWorkerBinding(t *testing.T) {
	var base string
	times := make(chan time.Time, 3)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != deviceGrantType || r.Form.Get("device_code") != "private-device-code" || r.Form.Get("client_id") != "device-client" || r.Form.Get("resource") != base+"/mcp" || r.Form.Get("scope") != workerOAuthScope || r.Form.Has("client_secret") || r.Header.Get("Authorization") != "" {
			t.Error("device polling crossed worker binding or sent unsupported client credentials")
		}
		times <- time.Now()
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"device-access","refresh_token":"device-refresh","token_type":"Bearer","expires_in":3600,"scope":"tools"}`))
		}
	}))
	defer server.Close()
	base = server.URL
	p := NewOAuthProvider(server.Client())
	metadata := mcpmanage.OAuthMetadata{Resource: base + "/mcp", Issuer: base, TokenEndpoint: base + "/token"}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone, Scopes: []string{workerOAuthScope}}
	started := time.Now()
	token, err := p.PollDevice(t.Context(), metadata, g, mcpmanage.GrantSecrets{}, mcpmanage.OAuthDevice{Code: "private-device-code", ExpiresAt: started.Add(20 * time.Second), Interval: 1})
	if err != nil || token.Secrets.AccessToken != "device-access" || token.Secrets.RefreshToken != "device-refresh" {
		t.Fatalf("supported device polling failed: %v", err)
	}
	first, second, third := <-times, <-times, <-times
	if first.Sub(started) < 800*time.Millisecond || second.Sub(first) < 800*time.Millisecond || third.Sub(second) < 5800*time.Millisecond {
		t.Fatal("polling ignored advertised interval or slow_down increase")
	}
}

func TestWorkerDevicePollingDenialExpiryAndCancellationStayBounded(t *testing.T) {
	for _, outcome := range []string{"access_denied", "expired_token", "deadline", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outcome == "deadline" || outcome == "cancel" {
					t.Error("poll sent after lifetime ended")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": outcome, "error_description": "private issuer explanation"})
			}))
			defer server.Close()
			p := NewOAuthProvider(server.Client())
			metadata := mcpmanage.OAuthMetadata{Resource: server.URL + "/mcp", Issuer: server.URL, TokenEndpoint: server.URL + "/token"}
			g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
			device := mcpmanage.OAuthDevice{Code: "private-device-code", ExpiresAt: time.Now().Add(time.Minute), Interval: 1}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if outcome == "deadline" {
				device.ExpiresAt = time.Now().Add(50 * time.Millisecond)
			}
			if outcome == "cancel" {
				cancel()
			}
			_, err := p.PollDevice(ctx, metadata, g, mcpmanage.GrantSecrets{}, device)
			want := mcpcmd.ErrAuthAttempt
			if outcome == "access_denied" {
				want = mcpcmd.ErrAuthRequired
			}
			if !errors.Is(err, want) {
				t.Fatalf("device terminal error unsafe or incorrect: %v", err)
			}
		})
	}
}

func TestWorkerDeviceNullResponseDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`null`))
	}))
	defer server.Close()
	p := NewOAuthProvider(server.Client())
	metadata := mcpmanage.OAuthMetadata{Resource: server.URL + "/mcp", Issuer: server.URL, TokenEndpoint: server.URL + "/token", DeviceAuthorizationEndpoint: server.URL + "/device", GrantTypes: []string{deviceGrantType}}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("remote null response crashed device start: %v", recovered)
		}
	}()
	_, err := p.BeginDevice(t.Context(), metadata, g, mcpmanage.GrantSecrets{})
	if !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Errorf("malformed device response accepted: %v", err)
	}
}
func TestWorkerCanceledDeviceBeginDoesNotSend(t *testing.T) {
	sent := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"private-device-code","user_code":"ABCD","verification_uri":"` + "http://localhost/verify" + `","expires_in":60}`))
	}))
	defer server.Close()
	p := NewOAuthProvider(server.Client())
	metadata := mcpmanage.OAuthMetadata{Resource: server.URL + "/mcp", Issuer: server.URL, TokenEndpoint: server.URL + "/token", DeviceAuthorizationEndpoint: server.URL + "/device", GrantTypes: []string{deviceGrantType}}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := p.BeginDevice(ctx, metadata, g, mcpmanage.GrantSecrets{})
	select {
	case <-sent:
		t.Error("already-canceled device attempt sent a protocol request")
	default:
	}
	if err == nil {
		t.Error("canceled device begin returned instructions")
	}
}

type heldDeviceStartTransport struct{ requested, cancelled chan struct{} }

func (t heldDeviceStartTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	close(t.requested)
	<-r.Context().Done()
	close(t.cancelled)
	return nil, r.Context().Err()
}
func TestWorkerDeviceBeginCancelsHTTPAlreadyInProgress(t *testing.T) {
	requested, cancelled := make(chan struct{}), make(chan struct{})
	p := NewOAuthProvider(&http.Client{Transport: heldDeviceStartTransport{requested: requested, cancelled: cancelled}})
	metadata := mcpmanage.OAuthMetadata{Resource: "https://resource.example.org/mcp", Issuer: "https://issuer.example.org", TokenEndpoint: "https://issuer.example.org/token", DeviceAuthorizationEndpoint: "https://issuer.example.org/device", GrantTypes: []string{deviceGrantType}}
	g := mcpcmd.Grant{Binding: mcpcmd.AuthBinding{Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: "device-client"}, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.BeginDevice(ctx, metadata, g, mcpmanage.GrantSecrets{}); done <- err }()
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("device request did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("device cancellation did not reach outbound HTTP")
	}
	if err := <-done; !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatalf("cancelled start returned unsafe result: %v", err)
	}
}
