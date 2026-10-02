package mcpmanage

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func browserHarness(t *testing.T) (*Authorizations, *grantMemoryStore, *grantOAuth, mcpcmd.Revision, mcpcmd.Authority) {
	t.Helper()
	grants, store, o := grantHarness(t)
	o.metadata.AuthorizationEndpoint = o.metadata.Issuer + "/authorize"
	o.metadata.GrantTypes = []string{"authorization_code"}
	o.metadata.ResponseTypes = []string{"code"}
	o.metadata.PKCEMethods = []string{"S256"}
	o.metadata.RequireIssuerParameter = true
	o.exchange = func(_ context.Context, _ mcpcmd.Grant, code, verifier string) (OAuthToken, error) {
		if code != "one-use-code" || verifier != "private-pkce-verifier" {
			t.Error("code/PKCE binding lost")
		}
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "browser-worker-access", RefreshToken: "browser-worker-refresh", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"tools"}}, nil
	}
	s, err := NewAuthorizations(grants, "https://backoffice.example.org/balda/mcp/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	r := mcpcmd.Revision{ID: "revision", ConnectionID: "worker", Definition: mcpcmd.Definition{OAuth: true, Transport: mcpcmd.TransportHTTP, URL: o.metadata.Resource, Scopes: []string{"tools"}}}
	return s, store, o, r, definitionCreate().Authority
}

func TestBrowserAttemptExpiresDuringExchangeAndCannotCommit(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(30 * time.Millisecond)
	s.mu.Lock()
	s.attempts[started.ID].ExpiresAt = expires
	s.mu.Unlock()
	o.exchange = func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error) {
		timer := time.NewTimer(time.Until(expires.Add(time.Millisecond)))
		defer timer.Stop()
		<-timer.C
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "late-token", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	before := len(store.writes)
	if _, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
		t.Fatalf("expired attempt completed: %v", err)
	}
	if len(store.writes) != before {
		t.Fatal("late exchange installed grant")
	}
}

func TestBrowserCancelAndRestartInvalidateAttemptsWithoutPrivateProjection(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-pkce-verifier", started.AuthorizationURL, browserState(t, started), "browser-worker-access"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private browser material entered ordinary projection")
		}
	}
	if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatal("two simultaneous attempts started for one connection")
	}
	before := len(store.writes)
	if err := s.Cancel(t.Context(), started.ID, a); err != nil {
		t.Fatal(err)
	}
	callback := mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
	if _, err := s.CompleteBrowser(t.Context(), callback); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
		t.Fatal("canceled state remained usable")
	}
	if len(store.writes) != before {
		t.Fatal("canceled attempt changed grant")
	}
	started, err = s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	restarted, err := NewAuthorizations(s.grants, s.redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	callback.State = browserState(t, started)
	if _, err := restarted.CompleteBrowser(t.Context(), callback); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
		t.Fatal("unfinished attempt survived restart")
	}
}

func TestBrowserDisconnectFencesExchangeAlreadyInProgress(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	exchanging, release := make(chan struct{}), make(chan struct{})
	o.exchange = func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error) {
		close(exchanging)
		<-release
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "stale-code-access", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
		done <- err
	}()
	select {
	case <-exchanging:
	case <-time.After(time.Second):
		t.Fatal("code exchange not dispatched")
	}
	err = s.Disconnect(t.Context(), r.ConnectionID, a)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, mcpcmd.ErrAuthAttempt) && !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("disconnected exchange revived worker grant: %v", err)
	}
	g, found, err := store.GetMCPGrant(t.Context(), mcpcmd.AuthBinding{ConnectionID: r.ConnectionID, Resource: o.metadata.Resource, Issuer: o.metadata.Issuer, ClientID: "worker-client"})
	if err != nil || !found || g.Status != mcpcmd.GrantDisconnected {
		t.Fatal("disconnected grant revived")
	}
}

func TestBrowserCallbackRejectsChangedGrantGenerationBeforeExchange(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	client := OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}
	started, err := s.BeginBrowser(t.Context(), r, "", client, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.grants.PrepareAuthorization(t.Context(), r, "", s.redirectURI, client, a); err != nil {
		t.Fatal(err)
	}
	o.exchange = func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error) {
		t.Error("stale attempt exchanged code")
		return OAuthToken{}, mcpcmd.ErrUnavailable
	}
	before := len(store.writes)
	if _, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	if len(store.writes) != before {
		t.Fatal("stale callback installed grant")
	}
}

func TestBrowserAuthorizationInstallsWorkerGrantAndConsumesStateOnce(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil || started.AuthorizationURL == "" {
		t.Fatalf("browser authorization did not start: %v", err)
	}
	callback := mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
	g, err := s.CompleteBrowser(t.Context(), callback)
	if err != nil || g.Status != mcpcmd.GrantAuthorized || g.ProtectedValues != nil {
		t.Fatalf("worker authorization did not complete safely: %v", err)
	}
	plain, err := s.grants.RequestCredentials(t.Context(), g.Binding, r.Definition.Scopes)
	if err != nil || plain.AccessToken != "browser-worker-access" {
		t.Fatal("completed browser grant not usable by worker")
	}
	before := len(store.writes)
	if _, err := s.CompleteBrowser(t.Context(), callback); err == nil {
		t.Fatal("replayed state installed another grant")
	}
	if len(store.writes) != before {
		t.Fatal("replayed callback changed credentials")
	}
}

func TestBrowserCallbackRejectsWrongIssuerBrowserAndAuthorityVersions(t *testing.T) {
	for _, change := range []func(*mcpcmd.BrowserCallback){
		func(c *mcpcmd.BrowserCallback) { c.Issuer = "https://wrong-issuer.example.org" },
		func(c *mcpcmd.BrowserCallback) { c.Issuer = "" },
		func(c *mcpcmd.BrowserCallback) { c.Authority.SessionID = "different-browser" },
		func(c *mcpcmd.BrowserCallback) { c.Authority.UserVersion++ },
		func(c *mcpcmd.BrowserCallback) { c.Authority.CredentialVersion++ },
		func(c *mcpcmd.BrowserCallback) { c.Authority.MFAVersion++ },
		func(c *mcpcmd.BrowserCallback) { c.Denied = true },
	} {
		s, store, o, r, a := browserHarness(t)
		started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
		if err != nil {
			t.Fatalf("browser begin failed: %v", err)
		}
		before := len(store.writes)
		callback := mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
		change(&callback)
		if _, err := s.CompleteBrowser(t.Context(), callback); err == nil || errors.Is(err, mcpcmd.ErrUnavailable) {
			t.Fatalf("callback binding not enforced: %v", err)
		}
		if len(store.writes) != before {
			t.Fatal("invalid callback installed grant")
		}
	}
}

func TestBrowserFlowRequiresIssuerResponseCapabilityBeforeRegistration(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	o.metadata.RequireIssuerParameter = false
	before := len(store.writes)
	if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a); !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatalf("unbound browser issuer capability accepted: %v", err)
	}
	if len(store.writes) != before {
		t.Fatal("unsupported browser flow replaced current grant")
	}
}

func browserState(t *testing.T, started mcpcmd.BrowserAuthorization) string {
	t.Helper()
	u, err := url.Parse(started.AuthorizationURL)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatal("authorization URL has no protocol state")
	}
	return u.Query().Get("state")
}

type browserCommitGateStore struct {
	*grantMemoryStore
	entered, release chan struct{}
}

func (s *browserCommitGateStore) SaveGrant(ctx context.Context, m GrantMutation) error {
	if m.Operation == mcpcmd.GrantAuthorize {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Model SQL driver commit after database/sql's context check: cancellation
		// cannot recall the already dispatched commit.
		close(s.entered)
		<-s.release
	}
	return s.grantMemoryStore.SaveGrant(ctx, m)
}
func TestBrowserCancellationCannotReportSuccessAfterCommitStarts(t *testing.T) {
	s, memory, o, r, a := browserHarness(t)
	defer s.Close()
	store := &browserCommitGateStore{grantMemoryStore: memory, entered: make(chan struct{}), release: make(chan struct{})}
	s.grants.store = store
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
		completed <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("commit not dispatched")
	}
	cancelled := make(chan error, 1)
	go func() { cancelled <- s.Cancel(t.Context(), started.ID, a) }()
	var cancelErr error
	waiting := false
	select {
	case cancelErr = <-cancelled:
	case <-time.After(50 * time.Millisecond):
		waiting = true
	}
	close(store.release)
	completeErr := <-completed
	if waiting {
		cancelErr = <-cancelled
	}
	if cancelErr == nil && completeErr == nil {
		t.Fatal("cancel succeeded while completion committed an authorized grant")
	}
}

func TestBrowserUnsupportedCapabilityPreservesWorkerGrant(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	defer s.Close()
	o.metadata.AuthorizationEndpoint = ""
	before := store.grant
	writes := len(store.writes)
	if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a); err == nil {
		t.Fatal("missing authorization endpoint accepted")
	}
	if len(store.writes) != writes || store.grant.Generation != before.Generation || store.grant.Status != before.Status {
		t.Fatal("unsupported browser begin cleared existing worker authorization")
	}
}

type browserAuthorityExpiryStore struct {
	*grantMemoryStore
	expires time.Time
}

func (s *browserAuthorityExpiryStore) CheckMCPAuthority(ctx context.Context, a mcpcmd.Authority) error {
	if !a.At.Before(s.expires) {
		return mcpcmd.ErrForbidden
	}
	return s.grantMemoryStore.CheckMCPAuthority(ctx, a)
}
func (s *browserAuthorityExpiryStore) SaveGrant(ctx context.Context, m GrantMutation) error {
	if err := s.CheckMCPAuthority(ctx, *m.Authority); err != nil {
		return err
	}
	return s.grantMemoryStore.SaveGrant(ctx, m)
}
func TestBrowserQueuedCompletionRechecksAuthorityAtCommit(t *testing.T) {
	s, memory, o, r, a := browserHarness(t)
	defer s.Close()
	store := &browserAuthorityExpiryStore{grantMemoryStore: memory, expires: time.Now().Add(time.Hour)}
	s.grants.store = store
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	gated := make(chan struct{})
	o.exchange = func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error) {
		s.mu.Lock()
		close(gated)
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "queued-token", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	completed := make(chan error, 1)
	go func() {
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
		completed <- err
	}()
	select {
	case <-gated:
	case <-time.After(time.Second):
		t.Fatal("exchange did not reach finalization gate")
	}
	// Let completion reach the finalization mutex before the canonical authority
	// expires, just as it would while another grant commit owns that mutex.
	timer := time.NewTimer(30 * time.Millisecond)
	<-timer.C
	store.expires = time.Now().UTC()
	s.mu.Unlock()
	if err := <-completed; !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("authority expired while queued but grant was committed: %v", err)
	}
	if store.grant.Status != mcpcmd.GrantAuthRequired {
		t.Fatal("expired queued authority installed credentials")
	}
}
