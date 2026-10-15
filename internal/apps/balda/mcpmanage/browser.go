package mcpmanage

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// Authorizations owns transient browser/device attempts for this installation.
type Authorizations struct {
	grants       *Grants
	definitions  authorizationBinding
	redirectURI  string
	mu           sync.Mutex
	attempts     map[string]*authorizationAttempt
	byConnection map[string]string
	byState      map[string]string
	ctx          context.Context
	cancel       context.CancelFunc
	stopped      bool
	polls        sync.WaitGroup
}

const authorizationAttemptTTL = 10 * time.Minute
const maxAuthorizationAttempts = 128

type authorizationAttempt struct {
	Flow                              authorizationFlow
	Device                            mcpcmd.DeviceAuthorization
	ID, State, ConnectionID, Verifier string
	ExpiresAt                         time.Time
	Authority                         mcpcmd.Authority
	Grant                             mcpcmd.Grant
	RevisionID                        string
	Metadata                          OAuthMetadata
	Ready, Consumed                   bool
	ctx                               context.Context
	cancel                            context.CancelFunc
}

type authorizationBinding interface {
	BindAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error)
}

// NewAuthorizations binds native callback policy to trusted grant storage.
func NewAuthorizations(grants *Grants, redirectURI string, definitions authorizationBinding) (*Authorizations, error) {
	if grants == nil || definitions == nil || (redirectURI != "" && !validRemoteURL(redirectURI)) {
		return nil, mcpcmd.ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Authorizations{grants: grants, definitions: definitions, redirectURI: redirectURI, attempts: make(map[string]*authorizationAttempt), byConnection: make(map[string]string), byState: make(map[string]string), ctx: ctx, cancel: cancel}, nil
}

// BeginBrowser starts one worker authorization attempt for the current browser.
func (s *Authorizations) BeginBrowser(ctx context.Context, r mcpcmd.Revision, metadataURL string, client OAuthClient, authority mcpcmd.Authority) (mcpcmd.BrowserAuthorization, error) {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.BrowserAuthorization{}, safeOperationError(err)
	}
	if r.ConnectionID == "" || !validRemoteURL(s.redirectURI) {
		return mcpcmd.BrowserAuthorization{}, mcpcmd.ErrInvalid
	}
	a, err := s.reserve(r.ConnectionID, authority, authorizationBrowser)
	if err != nil {
		return mcpcmd.BrowserAuthorization{}, err
	}
	complete := false
	defer func() {
		if !complete {
			s.finish(a)
		}
	}()
	ctx, stop := bindAttemptContext(ctx, a.ctx)
	defer stop()
	g, metadata, err := s.grants.prepareAuthorization(ctx, r, metadataURL, s.redirectURI, client, authority, authorizationBrowser)
	if err != nil {
		return mcpcmd.BrowserAuthorization{}, err
	}
	location, verifier, err := s.grants.oauth.BeginCode(metadata, g, s.redirectURI, a.State)
	if err != nil {
		return mcpcmd.BrowserAuthorization{}, safeOperationError(err)
	}
	if location == "" || verifier == "" {
		return mcpcmd.BrowserAuthorization{}, mcpcmd.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts[a.ID] != a || a.ctx.Err() != nil {
		return mcpcmd.BrowserAuthorization{}, mcpcmd.ErrAuthAttempt
	}
	a.Grant, a.Metadata, a.Verifier, a.Ready, a.RevisionID = g, metadata, verifier, true, r.ID
	complete = true
	return mcpcmd.BrowserAuthorization{ID: a.ID, ConnectionID: a.ConnectionID, AuthorizationURL: location, ExpiresAt: a.ExpiresAt}, nil
}

// CompleteBrowser consumes protocol state once before code exchange and commit.
func (s *Authorizations) CompleteBrowser(ctx context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Grant, error) {
	completionCtx := ctx
	s.mu.Lock()
	s.expireLocked()
	a := s.attempts[s.byState[callback.State]]
	if a == nil || a.Flow != authorizationBrowser || !a.Ready || a.Consumed {
		s.mu.Unlock()
		return mcpcmd.Grant{}, mcpcmd.ErrAuthAttempt
	}
	a.Consumed = true
	snapshot := *a
	s.mu.Unlock()
	defer s.finish(a)
	if !sameBrowserAuthority(snapshot.Authority, callback.Authority) {
		return mcpcmd.Grant{}, mcpcmd.ErrForbidden
	}
	if callback.Issuer == "" || callback.Issuer != snapshot.Grant.Binding.Issuer {
		return mcpcmd.Grant{}, mcpcmd.ErrInvalid
	}
	if callback.Denied {
		return mcpcmd.Grant{}, mcpcmd.ErrAuthRequired
	}
	if callback.Code == "" || len(callback.Code) > 16<<10 || strings.ContainsAny(callback.Code, "\x00\r\n") {
		return mcpcmd.Grant{}, mcpcmd.ErrInvalid
	}
	ctx, stop := bindAttemptContext(ctx, snapshot.ctx)
	defer stop()
	callback.Authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, callback.Authority); err != nil {
		return mcpcmd.Grant{}, safeOperationError(err)
	}
	g, found, err := s.grants.store.GetMCPGrant(ctx, snapshot.Grant.Binding)
	if err != nil {
		return mcpcmd.Grant{}, safeOperationError(err)
	}
	if !found || g.Generation != snapshot.Grant.Generation || g.Status != mcpcmd.GrantAuthRequired {
		return mcpcmd.Grant{}, mcpcmd.ErrConflict
	}
	secrets, err := s.grants.credentials.OpenGrant(g)
	if err != nil {
		return mcpcmd.Grant{}, err
	}
	if !validOAuthClient(OAuthClient{ID: g.Binding.ClientID, Secret: secrets.ClientSecret, AuthMethod: g.TokenEndpointAuthMethod, SecretExpiresAt: g.ClientSecretExpiresAt}, snapshot.Metadata, time.Now()) {
		return mcpcmd.Grant{}, mcpcmd.ErrAuthRequired
	}
	ctx, expireCancel := context.WithDeadline(ctx, snapshot.ExpiresAt)
	defer expireCancel()
	exchangeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := s.grants.oauth.ExchangeCode(exchangeCtx, snapshot.Metadata, g, secrets, s.redirectURI, callback.Code, snapshot.Verifier)
	if err != nil {
		return mcpcmd.Grant{}, safeOperationError(err)
	}
	if ctx.Err() != nil || snapshot.ctx.Err() != nil || !snapshot.ExpiresAt.After(time.Now()) {
		return mcpcmd.Grant{}, mcpcmd.ErrAuthAttempt
	}
	token.Secrets.ClientSecret = secrets.ClientSecret
	updated := g
	updated.Generation++
	updated.Status = mcpcmd.GrantAuthorized
	updated.AccessExpiresAt = token.ExpiresAt
	if !validGrantSecrets(updated, token.Secrets) || (!token.ExpiresAt.IsZero() && !token.ExpiresAt.After(time.Now())) || (token.Scopes != nil && !containsScopes(token.Scopes, g.Scopes)) {
		return mcpcmd.Grant{}, mcpcmd.ErrAuthRequired
	}
	// Cancellation and commit have one winner. Protocol I/O finishes before this
	// lock; a successful cancel cannot race an already dispatched storage commit.
	err = func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attempts[a.ID] != a || a.ctx.Err() != nil || !a.ExpiresAt.After(time.Now()) {
			return mcpcmd.ErrAuthAttempt
		}
		if !validOAuthClient(OAuthClient{ID: g.Binding.ClientID, Secret: secrets.ClientSecret, AuthMethod: g.TokenEndpointAuthMethod, SecretExpiresAt: g.ClientSecretExpiresAt}, snapshot.Metadata, time.Now()) {
			return mcpcmd.ErrAuthRequired
		}
		callback.Authority.At = time.Now().UTC()
		updated.UpdatedAt = callback.Authority.At
		if err := s.grants.save(ctx, g, updated, token.Secrets, mcpcmd.GrantAuthorize, &callback.Authority, snapshot.RevisionID); err != nil {
			return err
		}
		s.removeLocked(a)
		return nil
	}()
	if err != nil {
		return mcpcmd.Grant{}, err
	}
	updated.ProtectedValues = nil
	return updated, s.bindAuthorization(completionCtx, snapshot, callback.Authority)
}

// Grant installation is already durable here. Discovery may request its
// credentials, so binding must run outside attempt and grant-refresh locks.
func (s *Authorizations) bindAuthorization(ctx context.Context, a authorizationAttempt, authority mcpcmd.Authority) error {
	_, err := s.definitions.BindAuthorization(ctx, mcpcmd.SelectAuthorization{ConnectionID: a.ConnectionID, ExpectedRevisionID: a.RevisionID, Binding: a.Grant.Binding, Authority: authority})
	return safeOperationError(err)
}

func sameBrowserAuthority(a, b mcpcmd.Authority) bool {
	return a.UserID == b.UserID && a.SessionID == b.SessionID && a.UserVersion == b.UserVersion && a.CredentialVersion == b.CredentialVersion && a.MFAVersion == b.MFAVersion && a.SessionVersion == b.SessionVersion
}

func bindAttemptContext(ctx, attemptCtx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(attemptCtx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (s *Authorizations) finish(a *authorizationAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts[a.ID] == a {
		s.removeLocked(a)
	}
}
func (s *Authorizations) removeLocked(a *authorizationAttempt) {
	delete(s.attempts, a.ID)
	delete(s.byState, a.State)
	if s.byConnection[a.ConnectionID] == a.ID {
		delete(s.byConnection, a.ConnectionID)
	}
	a.cancel()
}
func (s *Authorizations) expireLocked() {
	now := time.Now()
	for _, a := range s.attempts {
		if !a.ExpiresAt.After(now) {
			s.removeLocked(a)
		}
	}
}

// Cancel requires the same canonical browser authority as the active attempt.
func (s *Authorizations) Cancel(ctx context.Context, id string, authority mcpcmd.Authority) error {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return safeOperationError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	a := s.attempts[id]
	if a == nil || (a.Flow == authorizationDevice && a.Consumed) {
		return mcpcmd.ErrAuthAttempt
	}
	if !sameBrowserAuthority(a.Authority, authority) {
		return mcpcmd.ErrForbidden
	}
	s.removeLocked(a)
	return nil
}

// CurrentAttempt reads transient metadata only for the initiating browser.
func (s *Authorizations) CurrentAttempt(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error) {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.AuthorizationAttempt{}, false, safeOperationError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	a := s.attempts[s.byConnection[connectionID]]
	if a == nil {
		return mcpcmd.AuthorizationAttempt{}, false, nil
	}
	if !sameBrowserAuthority(a.Authority, authority) {
		// A current administrator can still read connection management while
		// another browser owns the private authorization handoff.
		return mcpcmd.AuthorizationAttempt{}, false, nil
	}
	return mcpcmd.AuthorizationAttempt{ID: a.ID, ConnectionID: a.ConnectionID, ExpiresAt: a.ExpiresAt, Device: a.Flow == authorizationDevice}, true, nil
}

// Disconnect cancels transient attempts and revokes every retained grant context.
func (s *Authorizations) Disconnect(ctx context.Context, connectionID string, authority mcpcmd.Authority) error {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return safeOperationError(err)
	}
	s.mu.Lock()
	if a := s.attempts[s.byConnection[connectionID]]; a != nil {
		s.removeLocked(a)
	}
	s.mu.Unlock()
	return s.grants.DisconnectConnection(ctx, connectionID, authority)
}

// Close invalidates pending attempts and cancels in-flight protocol operations.
func (s *Authorizations) Close() {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	for _, a := range s.attempts {
		s.removeLocked(a)
	}
	s.mu.Unlock()
	s.polls.Wait()
}

// A shared callback URI requires an issuer response to prevent server mix-up.
func supportsBrowserAuthorization(metadata OAuthMetadata) bool {
	return metadata.RequireIssuerParameter && validRemoteURL(metadata.AuthorizationEndpoint) && slices.Contains(metadata.PKCEMethods, "S256") && (len(metadata.GrantTypes) == 0 || slices.Contains(metadata.GrantTypes, "authorization_code")) && (len(metadata.ResponseTypes) == 0 || slices.Contains(metadata.ResponseTypes, "code"))
}

type authorizationFlow uint8

const (
	authorizationRegistration authorizationFlow = iota
	authorizationBrowser
	authorizationDevice
)

func (s *Authorizations) reserve(connectionID string, authority mcpcmd.Authority, flow authorizationFlow) (*authorizationAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.stopped {
		return nil, mcpcmd.ErrUnavailable
	}
	if len(s.attempts) >= maxAuthorizationAttempts || s.byConnection[connectionID] != "" {
		return nil, mcpcmd.ErrConflict
	}
	expires := time.Now().Add(authorizationAttemptTTL)
	attemptCtx, cancel := context.WithDeadline(s.ctx, expires)
	a := &authorizationAttempt{ID: rand.Text(), ConnectionID: connectionID, Flow: flow, ExpiresAt: expires, Authority: authority, ctx: attemptCtx, cancel: cancel}
	s.attempts[a.ID] = a
	s.byConnection[connectionID] = a.ID
	if flow == authorizationBrowser {
		a.State = rand.Text()
		s.byState[a.State] = a.ID
	}
	return a, nil
}
