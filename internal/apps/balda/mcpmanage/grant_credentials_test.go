package mcpmanage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestWorkerGrantEncryptionBindsIdentityAndGeneration(t *testing.T) {
	s, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	g := grantCredentialFixture()
	secrets := GrantSecrets{AccessToken: "worker-access-token-fixture", RefreshToken: "worker-refresh-token-fixture", ClientSecret: "worker-client-secret-fixture", TokenType: "Bearer"}
	g.ProtectedValues, err = s.ProtectGrant(g, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secrets.AccessToken, secrets.RefreshToken, secrets.ClientSecret} {
		if bytes.Contains(g.ProtectedValues, []byte(secret)) {
			t.Fatal("plaintext grant credential at rest")
		}
	}
	opened, err := s.OpenGrant(g)
	if err != nil || opened != secrets {
		t.Fatalf("worker grant credentials unavailable: %v", err)
	}
	data, err := json.Marshal(struct {
		Grant   mcpcmd.Grant
		Secrets GrantSecrets
	}{g, secrets})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secrets.AccessToken, secrets.RefreshToken, secrets.ClientSecret} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("worker credential entered public JSON")
		}
	}
	for _, change := range []func(*mcpcmd.Grant){func(g *mcpcmd.Grant) { g.ID = "other-grant" }, func(g *mcpcmd.Grant) { g.Binding.ConnectionID = "other-worker" }, func(g *mcpcmd.Grant) { g.Binding.Resource = "https://other.example.org" }, func(g *mcpcmd.Grant) { g.Binding.Issuer = "https://other-issuer.example.org" }, func(g *mcpcmd.Grant) { g.Binding.ClientID = "other-client" }, func(g *mcpcmd.Grant) { g.Generation++ }, func(g *mcpcmd.Grant) {
		g.ProtectedValues = append([]byte(nil), g.ProtectedValues...)
		g.ProtectedValues[len(g.ProtectedValues)-1] ^= 1
	}} {
		altered := g
		change(&altered)
		if _, err := s.OpenGrant(altered); !errors.Is(err, mcpcmd.ErrCredentials) {
			t.Fatalf("grant crossed authentication boundary: %v", err)
		}
	}
	withoutKey, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutKey.ProtectGrant(g, secrets); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("protected grant write allowed without key: %v", err)
	}
	if _, err := withoutKey.OpenGrant(g); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("stored grant opened without key: %v", err)
	}
}

func grantCredentialFixture() mcpcmd.Grant {
	now := time.Now().UTC()
	return mcpcmd.Grant{ID: "grant", Binding: mcpcmd.AuthBinding{ConnectionID: "worker", Resource: "https://worker.example.org/mcp", Issuer: "https://issuer.example.org", ClientID: "worker-client"}, Generation: 2, Status: mcpcmd.GrantAuthorized, TokenEndpointAuthMethod: "client_secret_basic", CreatedAt: now, UpdatedAt: now}
}

func TestCredentialReadinessIncludesRetainedWorkerGrants(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	s, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	g := grantCredentialFixture()
	g.ProtectedValues, err = s.ProtectGrant(g, GrantSecrets{AccessToken: "startup-worker-token", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))} {
		service, err := New(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.ValidateCredentials(t.Context(), retainedGrantReader{g}); !errors.Is(err, mcpcmd.ErrCredentials) {
			t.Fatalf("protected grant bypassed startup key validation: %v", err)
		}
	}
	restarted, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ValidateCredentials(t.Context(), retainedGrantReader{g}); err != nil {
		t.Fatalf("persisted worker grant lost after restart: %v", err)
	}
}

type retainedGrantReader []mcpcmd.Grant

func (r retainedGrantReader) ListMCPGrants(context.Context) ([]mcpcmd.Grant, error) { return r, nil }
func (retainedGrantReader) ListMCPRevisions(context.Context) ([]mcpcmd.Revision, error) {
	return nil, nil
}
