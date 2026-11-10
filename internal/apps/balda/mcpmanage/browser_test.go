package mcpmanage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type authorizationBinder struct {
	bind func(context.Context, mcpcmd.SelectAuthorization) (mcpcmd.Item, error)
}

func (b authorizationBinder) BindAuthorization(ctx context.Context, r mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	if b.bind != nil {
		return b.bind(ctx, r)
	}
	return mcpcmd.Item{}, nil
}

func TestBrowserCompletionBindsSavedGrantOutsideAttemptLock(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	defer s.Close()
	bound := false
	s.definitions = authorizationBinder{bind: func(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
		bound = true
		if !s.mu.TryLock() {
			t.Fatal("binding held the attempt lock")
		}
		s.mu.Unlock()
		if request.ExpectedRevisionID != r.ID || request.ConnectionID != r.ConnectionID {
			t.Fatal("completion selected a different revision")
		}
		if _, err := s.grants.RequestCredentials(ctx, request.Binding, r.Definition.Scopes); err != nil {
			t.Fatalf("binding cannot acquire saved grant: %v", err)
		}
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}}
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
	if !bound || !errors.Is(err, mcpcmd.ErrUnavailable) || g.Status != mcpcmd.GrantAuthorized || store.grant.Status != mcpcmd.GrantAuthorized {
		t.Fatalf("saved grant and failed binding were not preserved: bound=%v, status=%s, err=%v", bound, g.Status, err)
	}
}

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
	s, err := NewAuthorizations(grants, "https://backoffice.example.org/balda/mcp/oauth/callback", authorizationBinder{})
	if err != nil {
		t.Fatal(err)
	}
	r := mcpcmd.Revision{ID: "revision", ConnectionID: "worker", Definition: mcpcmd.Definition{OAuth: true, Transport: mcpcmd.TransportHTTP, URL: o.metadata.Resource, Scopes: []string{"tools"}}}
	store.currentRevisionID = r.ID
	return s, store, o, r, definitionCreate().Authority
}

func TestBrowserDefinitionEditDuringExchangeCannotInstallGrant(t *testing.T) {
	s, store, o, r, a := browserHarness(t)
	defer s.Close()
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	exchange := o.exchange
	o.exchange = func(ctx context.Context, grant mcpcmd.Grant, code, verifier string) (OAuthToken, error) {
		store.mu.Lock()
		store.currentRevisionID = "edited-revision"
		store.mu.Unlock()
		return exchange(ctx, grant, code, verifier)
	}
	_, err = s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
	if !errors.Is(err, mcpcmd.ErrConflict) || store.grant.Status != mcpcmd.GrantAuthRequired || len(store.writes) != 1 {
		t.Fatalf("definition edit allowed stale completion: status=%s, err=%v", store.grant.Status, err)
	}
}

func TestCurrentAttemptIsSafeAndBoundToInitiatingBrowser(t *testing.T) {
	s, _, _, r, a := browserHarness(t)
	defer s.Close()
	started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	attempt, found, err := s.CurrentAttempt(t.Context(), r.ConnectionID, a)
	if err != nil || !found || attempt.ID != started.ID || attempt.Device {
		t.Fatalf("pending attempt = %+v/%v/%v", attempt, found, err)
	}
	other := a
	other.WebAuthSessionID = "other-browser"
	if hidden, found, err := s.CurrentAttempt(t.Context(), r.ConnectionID, other); err != nil || found || hidden.ID != "" {
		t.Fatal("another current browser could not read management without private attempt metadata")
	}
	if err := s.Cancel(t.Context(), attempt.ID, a); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.CurrentAttempt(t.Context(), r.ConnectionID, a); err != nil || found {
		t.Fatal("canceled attempt remained current")
	}
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
	if _, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
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
	callback := mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
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
	restarted, err := NewAuthorizations(s.grants, s.redirectURI, s.definitions)
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
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
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
	if _, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}); !errors.Is(err, mcpcmd.ErrConflict) {
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
	callback := mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
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
		func(c *mcpcmd.BrowserCallback) { c.Authority.WebAuthSessionID = "different-browser" },
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
		callback := mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}
		change(&callback)
		if _, err := s.CompleteBrowser(t.Context(), callback); err == nil || errors.Is(err, mcpcmd.ErrUnavailable) {
			t.Fatalf("callback binding not enforced: %v", err)
		}
		if len(store.writes) != before {
			t.Fatal("invalid callback installed grant")
		}
	}
}

func TestBrowserFlowStartsWithoutAdvertisedIssuerResponse(t *testing.T) {
	s, _, o, r, a := browserHarness(t)
	defer s.Close()
	o.metadata.RequireIssuerParameter = false
	if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a); err != nil {
		t.Fatalf("browser authorization without advertised issuer response did not start: %v", err)
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
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
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
	for _, advertised := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(*OAuthMetadata)
		}{
			{"missing authorization endpoint", func(m *OAuthMetadata) { m.AuthorizationEndpoint = "" }},
			{"missing S256", func(m *OAuthMetadata) { m.PKCEMethods = []string{"plain"} }},
			{"unsupported grant", func(m *OAuthMetadata) { m.GrantTypes = []string{"client_credentials"} }},
			{"unsupported response", func(m *OAuthMetadata) { m.ResponseTypes = []string{"token"} }},
		} {
			t.Run(fmt.Sprintf("%s/issuer=%v", tc.name, advertised), func(t *testing.T) {
				s, store, o, r, a := browserHarness(t)
				defer s.Close()
				o.metadata.RequireIssuerParameter = advertised
				tc.change(&o.metadata)
				before := store.grant
				writes := len(store.writes)
				if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{}, a); !errors.Is(err, mcpcmd.ErrUnavailable) {
					t.Fatalf("unsupported capability accepted: %v", err)
				}
				if o.registrationRedirect != "" || len(store.writes) != writes || store.grant.Generation != before.Generation || store.grant.Status != before.Status {
					t.Fatal("unsupported browser begin registered client or cleared existing worker authorization")
				}
			})
		}
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
		_, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{CallbackPath: "/balda/mcp/oauth/callback", State: browserState(t, started), Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a})
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

func TestBrowserIssuerCapabilityBindsRedirectAcrossRegistrationAndExchange(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		for _, prefix := range []string{"", "/balda"} {
			for _, suppliedIssuer := range []bool{false, true} {
				t.Run(fmt.Sprintf("advertised=%v/prefix=%s/issuer=%v", advertised, prefix, suppliedIssuer), func(t *testing.T) {
					s, store, o, r, a := browserHarness(t)
					defer s.Close()
					o.metadata.RequireIssuerParameter = advertised
					s.redirectURI = "https://backoffice.example.org" + prefix + "/mcp/oauth/callback"
					wantRedirect := s.redirectURI
					if !advertised {
						wantRedirect += fmt.Sprintf("/%x", sha256.Sum256([]byte(o.metadata.Issuer)))
					}
					started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{}, a)
					if err != nil {
						t.Fatalf("begin: %v", err)
					}
					if o.registrationRedirect != wantRedirect || o.authorizationRedirect != wantRedirect {
						t.Fatalf("redirects register=%q authorize=%q, want %q", o.registrationRedirect, o.authorizationRedirect, wantRedirect)
					}
					u, err := url.Parse(wantRedirect)
					if err != nil {
						t.Fatal(err)
					}
					callback := mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", CallbackPath: u.Path, IssuerPresent: suppliedIssuer, Authority: a}
					if suppliedIssuer {
						callback.Issuer = o.metadata.Issuer
					}
					// Discovery may change after start; the attempt owns its captured policy.
					o.metadata.RequireIssuerParameter = !advertised
					g, err := s.CompleteBrowser(t.Context(), callback)
					if advertised && !suppliedIssuer {
						if !errors.Is(err, mcpcmd.ErrInvalid) || o.exchangeRedirect != "" || store.grant.Status != mcpcmd.GrantAuthRequired {
							t.Fatalf("missing advertised issuer exchanged code: err=%v, redirect=%q", err, o.exchangeRedirect)
						}
						return
					}
					if err != nil || g.Status != mcpcmd.GrantAuthorized || o.exchangeRedirect != wantRedirect {
						t.Fatalf("completion status=%s, redirect=%q, err=%v", g.Status, o.exchangeRedirect, err)
					}
					writes := len(store.writes)
					if _, err := s.CompleteBrowser(t.Context(), callback); !errors.Is(err, mcpcmd.ErrAuthAttempt) || len(store.writes) != writes {
						t.Fatalf("callback replay: %v", err)
					}
				})
			}
		}
	}
}

func TestBrowserFallbackRejectsWrongIssuerAndCallbackBeforeExchange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*mcpcmd.BrowserCallback)
	}{
		{"explicit empty issuer", func(c *mcpcmd.BrowserCallback) { c.IssuerPresent = true }},
		{"wrong issuer", func(c *mcpcmd.BrowserCallback) { c.Issuer = "https://login.example.org" }},
		{"issuer trailing slash", func(c *mcpcmd.BrowserCallback) { c.Issuer = "https://issuer.example.org/" }},
		{"shared callback", func(c *mcpcmd.BrowserCallback) { c.CallbackPath = "/balda/mcp/oauth/callback" }},
		{"other issuer callback", func(c *mcpcmd.BrowserCallback) {
			c.CallbackPath = fmt.Sprintf("/balda/mcp/oauth/callback/%x", sha256.Sum256([]byte("https://other.example.org")))
		}},
		{"wrong prefix", func(c *mcpcmd.BrowserCallback) { c.CallbackPath = strings.TrimPrefix(c.CallbackPath, "/balda") }},
		{"empty path", func(c *mcpcmd.BrowserCallback) { c.CallbackPath = "" }},
		{"extra path", func(c *mcpcmd.BrowserCallback) { c.CallbackPath += "/extra" }},
		{"escaped path", func(c *mcpcmd.BrowserCallback) {
			c.CallbackPath = strings.Replace(c.CallbackPath, "callback", "%63allback", 1)
		}},
	} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/denied=%v", tc.name, denied), func(t *testing.T) {
				s, store, o, r, a := browserHarness(t)
				defer s.Close()
				o.metadata.RequireIssuerParameter = false
				started, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
				if err != nil {
					t.Fatal(err)
				}
				redirect, err := url.Parse(o.authorizationRedirect)
				if err != nil {
					t.Fatal(err)
				}
				callback := mcpcmd.BrowserCallback{State: browserState(t, started), Code: "one-use-code", CallbackPath: redirect.Path, Denied: denied, Authority: a}
				tc.change(&callback)
				if _, err := s.CompleteBrowser(t.Context(), callback); !errors.Is(err, mcpcmd.ErrInvalid) || o.exchangeRedirect != "" || store.grant.Status != mcpcmd.GrantAuthRequired {
					t.Fatalf("invalid callback accepted or exchanged: err=%v, redirect=%q", err, o.exchangeRedirect)
				}
			})
		}
	}
}

func TestBrowserRedirectIsStableAndDistinctForTrustedIssuers(t *testing.T) {
	first, err := browserRedirectURI("https://backoffice.example.org/balda/mcp/oauth/callback", OAuthMetadata{Issuer: "https://issuer.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	for _, issuer := range []string{"https://issuer.example.org", "https://other.example.org", "https://issuer.example.org/"} {
		got, err := browserRedirectURI("https://backoffice.example.org/balda/mcp/oauth/callback", OAuthMetadata{Issuer: issuer})
		if err != nil || (got == first) != (issuer == "https://issuer.example.org") {
			t.Fatalf("issuer %q callback=%q, first=%q, err=%v", issuer, got, first, err)
		}
	}
}

func TestBrowserRejectsAmbiguousRedirectBaseBeforeRegistration(t *testing.T) {
	for _, suffix := range []string{"?query=value", "?", "#fragment", "/", "/../callback", "//child", "/%63hild", "/child%20name"} {
		t.Run(suffix, func(t *testing.T) {
			s, store, o, r, a := browserHarness(t)
			defer s.Close()
			o.metadata.RequireIssuerParameter = false
			s.redirectURI += suffix
			writes := len(store.writes)
			if _, err := s.BeginBrowser(t.Context(), r, "", OAuthClient{}, a); !errors.Is(err, mcpcmd.ErrInvalid) || o.registrationRedirect != "" || len(store.writes) != writes {
				t.Fatalf("ambiguous callback registered or wrote grant: %v", err)
			}
		})
	}
}
