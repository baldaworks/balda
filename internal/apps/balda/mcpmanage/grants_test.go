package mcpmanage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type grantMemoryStore struct {
	mu         sync.Mutex
	grant      mcpcmd.Grant
	retained   []mcpcmd.Grant
	writes     []GrantMutation
	writeError error
}

func (s *grantMemoryStore) CheckMCPAuthority(_ context.Context, a mcpcmd.Authority) error {
	if a.UserID == "" || a.SessionID == "" {
		return mcpcmd.ErrForbidden
	}
	return nil
}
func (s *grantMemoryStore) GetMCPGrant(_ context.Context, binding mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.retained {
		if g.Binding == binding {
			return g, true, nil
		}
	}
	return s.grant, s.grant.Binding == binding && s.grant.ID != "", nil
}
func (s *grantMemoryStore) ListMCPGrants(context.Context) ([]mcpcmd.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]mcpcmd.Grant(nil), s.retained...)
	if s.grant.ID != "" {
		out = append(out, s.grant)
	}
	return out, nil
}
func (s *grantMemoryStore) SaveGrant(_ context.Context, m GrantMutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeError != nil {
		return s.writeError
	}
	for i, g := range s.retained {
		if g.Binding == m.Grant.Binding {
			if g.Generation != m.ExpectedGeneration {
				return mcpcmd.ErrConflict
			}
			s.retained[i] = m.Grant
			s.writes = append(s.writes, m)
			return nil
		}
	}
	if s.grant.Generation != m.ExpectedGeneration {
		return mcpcmd.ErrConflict
	}
	s.grant = m.Grant
	s.writes = append(s.writes, m)
	return nil
}

func TestWorkerConnectionDisconnectRevokesRetainedIdentityContexts(t *testing.T) {
	s, store, _ := grantHarness(t)
	old := store.grant
	old.ID = "historical-grant"
	old.Binding.Resource = "https://historical.example.org/mcp"
	var err error
	old.ProtectedValues, err = s.credentials.ProtectGrant(old, GrantSecrets{AccessToken: "historical-access", RefreshToken: "historical-refresh", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	store.retained = []mcpcmd.Grant{old}
	if err := s.DisconnectConnection(t.Context(), store.grant.Binding.ConnectionID, definitionCreate().Authority); err != nil {
		t.Fatalf("connection disconnect failed: %v", err)
	}
	for _, binding := range []mcpcmd.AuthBinding{store.grant.Binding, old.Binding} {
		if _, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrDisconnected) {
			t.Fatalf("retained context stayed active: %v", err)
		}
	}
}

type grantOAuth struct {
	metadata OAuthMetadata
	refresh  func(context.Context, mcpcmd.Grant, GrantSecrets) (OAuthToken, error)
	register func(context.Context) (OAuthClient, error)
	exchange func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error)
}

func (*grantOAuth) BeginCode(_ OAuthMetadata, _ mcpcmd.Grant, _ string, state string) (string, string, error) {
	return "https://issuer.example.org/authorize?state=" + state, "private-pkce-verifier", nil
}
func (o *grantOAuth) ExchangeCode(ctx context.Context, _ OAuthMetadata, g mcpcmd.Grant, _ GrantSecrets, _ string, code, verifier string) (OAuthToken, error) {
	return o.exchange(ctx, g, code, verifier)
}

func (o *grantOAuth) Discover(context.Context, string, string) (OAuthMetadata, error) {
	return o.metadata, nil
}
func (o *grantOAuth) DiscoverIssuer(context.Context, mcpcmd.AuthBinding) (OAuthMetadata, error) {
	return o.metadata, nil
}
func (o *grantOAuth) Register(ctx context.Context, _ OAuthMetadata, _ string, _ []string) (OAuthClient, error) {
	if o.register != nil {
		return o.register(ctx)
	}
	return OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, nil
}
func (o *grantOAuth) Refresh(ctx context.Context, _ OAuthMetadata, g mcpcmd.Grant, v GrantSecrets) (OAuthToken, error) {
	return o.refresh(ctx, g, v)
}

func grantHarness(t *testing.T) (*Grants, *grantMemoryStore, *grantOAuth) {
	t.Helper()
	credentials, _, _ := definitionHarness(t)
	g := grantCredentialFixture()
	g.Scopes = []string{"tools"}
	g.AccessExpiresAt = time.Now().Add(-time.Minute)
	g.TokenEndpointAuthMethod = mcpcmd.ClientAuthNone
	var err error
	g.ProtectedValues, err = credentials.credentials.ProtectGrant(g, GrantSecrets{AccessToken: "expired-access", RefreshToken: "original-refresh", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	store := &grantMemoryStore{grant: g}
	o := &grantOAuth{metadata: OAuthMetadata{Resource: g.Binding.Resource, Issuer: g.Binding.Issuer, TokenEndpoint: g.Binding.Issuer + "/token", Scopes: g.Scopes, AuthMethods: []string{mcpcmd.ClientAuthNone}}}
	s, err := NewGrants(credentials.credentials, store, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, o
}

func TestConcurrentWorkerRequestsPersistOneRotationAndRestart(t *testing.T) {
	s, store, o := grantHarness(t)
	var mu sync.Mutex
	refreshes := 0
	o.refresh = func(_ context.Context, _ mcpcmd.Grant, old GrantSecrets) (OAuthToken, error) {
		mu.Lock()
		defer mu.Unlock()
		refreshes++
		if old.RefreshToken != "original-refresh" {
			t.Error("unexpected refresh credential")
		}
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"tools"}}, nil
	}
	binding := store.grant.Binding
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := s.RequestCredentials(t.Context(), binding, []string{"tools"})
			if err != nil || got.AccessToken != "rotated-access" {
				t.Errorf("current credentials unavailable: %v", err)
			}
		})
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("external refresh count=%d, want 1", refreshes)
	}
	g, _, _ := store.GetMCPGrant(t.Context(), binding)
	plain, err := s.credentials.OpenGrant(g)
	if err != nil || plain.RefreshToken != "rotated-refresh" || g.Generation != 3 {
		t.Fatal("rotation was not durably protected before use")
	}
	restarted, err := NewGrants(s.credentials, store, o)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.RequestCredentials(t.Context(), binding, []string{"tools"})
	if err != nil || got.AccessToken != "rotated-access" || refreshes != 1 {
		t.Fatal("restart repeated worker authorization or refresh")
	}
}

func TestWorkerDisconnectWinsHeldRefreshAndBlocksLaterRequests(t *testing.T) {
	s, store, o := grantHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	o.refresh = func(ctx context.Context, _ mcpcmd.Grant, _ GrantSecrets) (OAuthToken, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return OAuthToken{}, ctx.Err()
		}
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "stale-rotation", RefreshToken: "stale-refresh", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	binding := store.grant.Binding
	done := make(chan error, 1)
	go func() { _, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); done <- err }()
	select {
	case <-started:
	case <-done:
		t.Fatal("refresh was not dispatched")
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	err := s.Disconnect(t.Context(), binding, 2, definitionCreate().Authority)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, mcpcmd.ErrConflict) && !errors.Is(err, mcpcmd.ErrDisconnected) {
		t.Fatalf("held refresh restored grant: %v", err)
	}
	if _, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrDisconnected) {
		t.Fatalf("request used disconnected grant: %v", err)
	}
	g, _, _ := store.GetMCPGrant(t.Context(), binding)
	if g.Status != mcpcmd.GrantDisconnected || g.Generation != 3 {
		t.Fatal("disconnect was overwritten")
	}
}

func TestWorkerRefreshFailureStatesAreSafeAndExplicit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    OAuthToken
		upstream error
		want     error
		status   mcpcmd.GrantStatus
	}{
		{"rejected rotation", OAuthToken{}, mcpcmd.ErrAuthRequired, mcpcmd.ErrAuthRequired, mcpcmd.GrantAuthRequired},
		{"transient issuer failure", OAuthToken{}, errors.New("private-issuer-body-token"), mcpcmd.ErrUnavailable, mcpcmd.GrantAuthorized},
		{"insufficient scopes", OAuthToken{Secrets: GrantSecrets{AccessToken: "new-access", TokenType: "Bearer"}, Scopes: []string{"other"}}, nil, mcpcmd.ErrAuthRequired, mcpcmd.GrantAuthRequired},
		{"empty returned scopes", OAuthToken{Secrets: GrantSecrets{AccessToken: "new-access", TokenType: "Bearer"}, Scopes: []string{}}, nil, mcpcmd.ErrAuthRequired, mcpcmd.GrantAuthRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, o := grantHarness(t)
			o.refresh = func(context.Context, mcpcmd.Grant, GrantSecrets) (OAuthToken, error) { return tc.token, tc.upstream }
			binding := store.grant.Binding
			if _, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); !errors.Is(err, tc.want) {
				t.Fatalf("unsafe failure state: %v", err)
			}
			g, _, _ := store.GetMCPGrant(t.Context(), binding)
			if g.Status != tc.status {
				t.Fatalf("readiness status=%s, want %s", g.Status, tc.status)
			}
			if tc.status == mcpcmd.GrantAuthRequired {
				plain, err := s.credentials.OpenGrant(g)
				if err != nil || plain.AccessToken != "" || plain.RefreshToken != "" {
					t.Fatal("rejected grant retained reusable tokens")
				}
			}
		})
	}
}

func TestWorkerRegistrationIsBoundAndProtectedBeforeAuthorization(t *testing.T) {
	s, store, o := grantHarness(t)
	store.grant = mcpcmd.Grant{}
	client := OAuthClient{ID: "installation-client", Secret: "worker-registration-secret", AuthMethod: mcpcmd.ClientAuthSecretBasic}
	o.metadata.AuthMethods = []string{mcpcmd.ClientAuthSecretBasic}
	r := mcpcmd.Revision{ID: "revision", ConnectionID: "worker", Definition: mcpcmd.Definition{OAuth: true, Transport: mcpcmd.TransportHTTP, URL: o.metadata.Resource, Scopes: []string{"tools"}}}
	g, metadata, err := s.PrepareAuthorization(t.Context(), r, "", "https://backoffice.example.org/callback", client, definitionCreate().Authority)
	if err != nil || g.Binding.ClientID != client.ID || g.Generation != 1 || g.Status != mcpcmd.GrantAuthRequired || metadata.Issuer != o.metadata.Issuer {
		t.Fatalf("supported registration failed: %v", err)
	}
	if g.ProtectedValues != nil {
		t.Fatal("registration returned encrypted/private payload to ordinary view")
	}
	durable, _, _ := store.GetMCPGrant(t.Context(), g.Binding)
	plain, err := s.credentials.OpenGrant(durable)
	if err != nil || plain.ClientSecret != client.Secret {
		t.Fatal("registration secret not durably protected")
	}
	if _, err := s.RequestCredentials(t.Context(), g.Binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
		t.Fatal("registration supplied credentials before valid flow completion")
	}
	for _, change := range []func(*OAuthClient){func(c *OAuthClient) { c.Secret = "" }, func(c *OAuthClient) { c.SecretExpiresAt = time.Now().Add(-time.Second) }, func(c *OAuthClient) { c.AuthMethod = mcpcmd.ClientAuthNone }} {
		bad := client
		change(&bad)
		if _, _, err := s.PrepareAuthorization(t.Context(), r, "", "https://backoffice.example.org/callback", bad, definitionCreate().Authority); !errors.Is(err, mcpcmd.ErrInvalid) {
			t.Fatalf("invalid registration accepted: %v", err)
		}
	}
	o.metadata.Resource = "https://different-resource.example.org/mcp"
	if _, _, err := s.PrepareAuthorization(t.Context(), r, "", "https://backoffice.example.org/callback", client, definitionCreate().Authority); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatal("resource mismatch installed client")
	}
}

func TestFreshWorkerCredentialsRequireUsableClientRegistration(t *testing.T) {
	for _, tc := range []struct {
		method, secret string
		expires        time.Time
	}{
		{mcpcmd.ClientAuthSecretBasic, "", time.Time{}},
		{mcpcmd.ClientAuthNone, "unexpected-client-secret", time.Time{}},
		{mcpcmd.ClientAuthSecretPost, "expired-client-secret", time.Now().Add(-time.Minute)},
	} {
		t.Run(tc.method, func(t *testing.T) {
			s, store, o := grantHarness(t)
			g := store.grant
			g.AccessExpiresAt = time.Now().Add(time.Hour)
			g.TokenEndpointAuthMethod, g.ClientSecretExpiresAt = tc.method, tc.expires
			var err error
			g.ProtectedValues, err = s.credentials.ProtectGrant(g, GrantSecrets{AccessToken: "fresh-but-unusable", RefreshToken: "refresh", TokenType: "Bearer", ClientSecret: tc.secret})
			if err != nil {
				t.Fatal(err)
			}
			store.grant = g
			o.refresh = func(context.Context, mcpcmd.Grant, GrantSecrets) (OAuthToken, error) {
				t.Error("invalid registration attempted renewal")
				return OAuthToken{}, mcpcmd.ErrUnavailable
			}
			if _, err := s.RequestCredentials(t.Context(), g.Binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
				t.Fatalf("unusable registration supplied credentials: %v", err)
			}
		})
	}
}

func TestWorkerGrantMustMeetPinnedDefinitionScopes(t *testing.T) {
	s, store, _ := grantHarness(t)
	if _, err := s.RequestCredentials(t.Context(), store.grant.Binding, []string{"tools", "additional-pinned-scope"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
		t.Fatalf("grant supplied a wider pinned definition: %v", err)
	}
	if len(store.writes) != 0 {
		t.Fatal("pin-specific scope failure invalidated an otherwise valid narrower grant")
	}
	store.grant.Scopes = nil
	store.grant.AccessExpiresAt = time.Now().Add(time.Hour)
	if _, err := s.RequestCredentials(t.Context(), store.grant.Binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
		t.Fatalf("empty durable grant scopes supplied a scoped pin: %v", err)
	}
}

func TestWorkerRotationIsNotReturnedBeforeDurableSave(t *testing.T) {
	s, store, o := grantHarness(t)
	store.writeError = errors.New("storage-private-body")
	rotated := false
	o.refresh = func(context.Context, mcpcmd.Grant, GrantSecrets) (OAuthToken, error) {
		if rotated {
			return OAuthToken{}, mcpcmd.ErrAuthRequired
		}
		rotated = true
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "lost-access", RefreshToken: "lost-refresh", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	binding := store.grant.Binding
	if got, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrUnavailable) || got.AccessToken != "" {
		t.Fatal("uncommitted rotation escaped into MCP execution")
	}
	store.writeError = nil
	if _, err := s.RequestCredentials(t.Context(), binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrAuthRequired) {
		t.Fatal("lost rotation did not require explicit reauthorization")
	}
	g, _, _ := store.GetMCPGrant(t.Context(), binding)
	if g.Status != mcpcmd.GrantAuthRequired {
		t.Fatal("lost rotation remained ready")
	}
}

func TestWorkerDisconnectCannotMissRegistrationAlreadyInProgress(t *testing.T) {
	s, store, o := grantHarness(t)
	store.grant = mcpcmd.Grant{}
	started, release := make(chan struct{}), make(chan struct{})
	o.register = func(ctx context.Context) (OAuthClient, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return OAuthClient{}, ctx.Err()
		}
		return OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, nil
	}
	r := mcpcmd.Revision{ID: "revision", ConnectionID: "worker", Definition: mcpcmd.Definition{OAuth: true, Transport: mcpcmd.TransportHTTP, URL: o.metadata.Resource, Scopes: []string{"tools"}}}
	registered := make(chan error, 1)
	go func() {
		_, _, err := s.PrepareAuthorization(t.Context(), r, "", "https://backoffice.example.org/callback", OAuthClient{}, definitionCreate().Authority)
		registered <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("registration not dispatched")
	}
	disconnected := make(chan error, 1)
	go func() {
		disconnected <- s.DisconnectConnection(t.Context(), r.ConnectionID, definitionCreate().Authority)
	}()
	close(release)
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
	g, found, err := store.GetMCPGrant(t.Context(), mcpcmd.AuthBinding{ConnectionID: r.ConnectionID, Resource: o.metadata.Resource, Issuer: o.metadata.Issuer, ClientID: "worker-client"})
	if err != nil || !found || g.Status != mcpcmd.GrantDisconnected || g.Generation != 2 {
		t.Fatal("disconnect missed an in-flight registration")
	}
}

type expiringGrantAuthority struct {
	*grantMemoryStore
	expires time.Time
}

func (s expiringGrantAuthority) CheckMCPAuthority(ctx context.Context, a mcpcmd.Authority) error {
	if !a.At.Before(s.expires) {
		return mcpcmd.ErrForbidden
	}
	return s.grantMemoryStore.CheckMCPAuthority(ctx, a)
}
func (s expiringGrantAuthority) SaveGrant(ctx context.Context, m GrantMutation) error {
	if m.Authority != nil {
		if err := s.CheckMCPAuthority(ctx, *m.Authority); err != nil {
			return err
		}
	}
	return s.grantMemoryStore.SaveGrant(ctx, m)
}
func TestWorkerRegistrationCannotCommitWithAuthorityExpiredDuringDiscovery(t *testing.T) {
	s, store, o := grantHarness(t)
	store.grant = mcpcmd.Grant{}
	authority := definitionCreate().Authority
	expires := time.Now().Add(30 * time.Millisecond)
	s.store = expiringGrantAuthority{grantMemoryStore: store, expires: expires}
	o.register = func(ctx context.Context) (OAuthClient, error) {
		timer := time.NewTimer(time.Until(expires.Add(time.Millisecond)))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return OAuthClient{}, ctx.Err()
		}
		return OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, nil
	}
	r := mcpcmd.Revision{ID: "revision", ConnectionID: "worker", Definition: mcpcmd.Definition{OAuth: true, Transport: mcpcmd.TransportHTTP, URL: o.metadata.Resource}}
	if _, _, err := s.PrepareAuthorization(t.Context(), r, "", "https://backoffice.example.org/callback", OAuthClient{}, authority); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("expired current authority committed: %v", err)
	}
	if len(store.writes) != 0 {
		t.Fatal("registration survived expired browser authority")
	}
}
