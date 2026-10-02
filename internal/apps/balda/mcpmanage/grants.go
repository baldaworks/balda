package mcpmanage

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// GrantMutation fences credentials independently from definition publication.
type GrantMutation struct {
	Grant              mcpcmd.Grant
	Operation          mcpcmd.GrantOperation
	ExpectedGeneration uint64
	Authority          *mcpcmd.Authority
	Audit              usercmd.AuditEvent
}

// GrantStore uses the shared database and canonical administrator fence.
type GrantStore interface {
	CheckMCPAuthority(ctx context.Context, authority mcpcmd.Authority) error
	GetMCPGrant(ctx context.Context, binding mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error)
	ListMCPGrants(ctx context.Context) ([]mcpcmd.Grant, error)
	SaveGrant(ctx context.Context, mutation GrantMutation) error
}

// OAuthMetadata contains validated public protocol endpoints, never credentials.
type OAuthMetadata struct {
	Resource, Issuer, AuthorizationEndpoint, TokenEndpoint, RegistrationEndpoint string
	Scopes, AuthMethods, GrantTypes, ResponseTypes, PKCEMethods                  []string
	RequireIssuerParameter                                                       bool
}

// OAuthClient is supplied explicitly or returned by supported registration.
type OAuthClient struct {
	ID              string
	Secret          string `json:"-"`
	AuthMethod      string
	SecretExpiresAt time.Time
}

// OAuthToken is write-only upstream token material at the trusted adapter seam.
type OAuthToken struct {
	Secrets   GrantSecrets `json:"-"`
	ExpiresAt time.Time
	Scopes    []string
}

// OAuthProvider reuses SDK discovery/registration and OAuth token primitives.
type OAuthProvider interface {
	Discover(ctx context.Context, resource, metadataURL string) (OAuthMetadata, error)
	DiscoverIssuer(ctx context.Context, binding mcpcmd.AuthBinding) (OAuthMetadata, error)
	Register(ctx context.Context, metadata OAuthMetadata, redirectURI string, scopes []string) (OAuthClient, error)
	Refresh(ctx context.Context, metadata OAuthMetadata, grant mcpcmd.Grant, secrets GrantSecrets) (OAuthToken, error)
	BeginCode(metadata OAuthMetadata, grant mcpcmd.Grant, redirectURI, state string) (authorizationURL, verifier string, err error)
	ExchangeCode(ctx context.Context, metadata OAuthMetadata, grant mcpcmd.Grant, secrets GrantSecrets, redirectURI, code, verifier string) (OAuthToken, error)
}

// Grants owns worker authorization; it does not publish catalog snapshots.
type Grants struct {
	credentials     *Service
	store           GrantStore
	oauth           OAuthProvider
	refreshLocks    sync.Map // AuthBinding -> *sync.Mutex; one installation writer.
	managementLocks sync.Map // ConnectionID -> *sync.Mutex; register/disconnect.
}

// NewGrants binds worker credential policy to existing persistence and SDKs.
func NewGrants(credentials *Service, store GrantStore, oauth OAuthProvider) (*Grants, error) {
	if credentials == nil || store == nil || oauth == nil {
		return nil, mcpcmd.ErrInvalid
	}
	return &Grants{credentials: credentials, store: store, oauth: oauth}, nil
}

// RequestCredentials obtains current credentials only for an exact auth binding.
func (s *Grants) RequestCredentials(ctx context.Context, binding mcpcmd.AuthBinding, requiredScopes []string) (GrantSecrets, error) {
	lock, _ := s.refreshLocks.LoadOrStore(binding, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return GrantSecrets{}, mcpcmd.ErrUnavailable
	}
	g, found, err := s.store.GetMCPGrant(ctx, binding)
	if err != nil {
		return GrantSecrets{}, safeOperationError(err)
	}
	if !found || g.Status == mcpcmd.GrantAuthRequired {
		return GrantSecrets{}, mcpcmd.ErrAuthRequired
	}
	if g.Status == mcpcmd.GrantDisconnected {
		return GrantSecrets{}, mcpcmd.ErrDisconnected
	}
	if !containsScopes(g.Scopes, requiredScopes) {
		return GrantSecrets{}, mcpcmd.ErrAuthRequired
	}
	secrets, err := s.credentials.OpenGrant(g)
	if err != nil {
		return GrantSecrets{}, err
	}
	now := time.Now().UTC()
	if !validOAuthClient(OAuthClient{ID: g.Binding.ClientID, Secret: secrets.ClientSecret, AuthMethod: g.TokenEndpointAuthMethod, SecretExpiresAt: g.ClientSecretExpiresAt}, OAuthMetadata{}, now) {
		return GrantSecrets{}, s.requireAuthorization(ctx, g, secrets)
	}
	if g.AccessExpiresAt.IsZero() || g.AccessExpiresAt.After(now.Add(30*time.Second)) {
		return secrets, nil
	}
	if secrets.RefreshToken == "" {
		return GrantSecrets{}, s.requireAuthorization(ctx, g, secrets)
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	metadata, err := s.oauth.DiscoverIssuer(refreshCtx, binding)
	if err != nil {
		return GrantSecrets{}, safeOperationError(err)
	}
	if !matchesGrantMetadata(g, metadata) {
		return GrantSecrets{}, s.requireAuthorization(ctx, g, secrets)
	}
	token, err := s.oauth.Refresh(refreshCtx, metadata, g, secrets)
	if err != nil {
		if errors.Is(err, mcpcmd.ErrAuthRequired) {
			return GrantSecrets{}, s.requireAuthorization(ctx, g, secrets)
		}
		return GrantSecrets{}, safeOperationError(err)
	}
	if token.Secrets.RefreshToken == "" {
		token.Secrets.RefreshToken = secrets.RefreshToken
	}
	token.Secrets.ClientSecret = secrets.ClientSecret
	if !validGrantSecrets(g, token.Secrets) || (!token.ExpiresAt.IsZero() && !token.ExpiresAt.After(time.Now())) || (token.Scopes != nil && !containsScopes(token.Scopes, g.Scopes)) {
		return GrantSecrets{}, s.requireAuthorization(ctx, g, secrets)
	}
	updated := g
	updated.Generation++
	updated.UpdatedAt = time.Now().UTC()
	updated.AccessExpiresAt = token.ExpiresAt
	if err := s.save(ctx, g, updated, token.Secrets, mcpcmd.GrantRenew, nil); err != nil {
		return GrantSecrets{}, err
	}
	return token.Secrets, nil
}

// Disconnect advances the durable generation independently of a held refresh.
func (s *Grants) Disconnect(ctx context.Context, binding mcpcmd.AuthBinding, expected uint64, authority mcpcmd.Authority) error {
	if err := s.store.CheckMCPAuthority(ctx, authority); err != nil {
		return safeOperationError(err)
	}
	g, found, err := s.store.GetMCPGrant(ctx, binding)
	if err != nil {
		return safeOperationError(err)
	}
	if !found {
		return mcpcmd.ErrNotFound
	}
	if g.Generation != expected {
		return mcpcmd.ErrConflict
	}
	secrets, err := s.credentials.OpenGrant(g)
	if err != nil {
		return err
	}
	updated := g
	updated.Generation++
	updated.Status = mcpcmd.GrantDisconnected
	updated.AccessExpiresAt = time.Time{}
	updated.UpdatedAt = authority.At
	secrets.AccessToken, secrets.RefreshToken, secrets.TokenType = "", "", ""
	return s.save(ctx, g, updated, secrets, mcpcmd.GrantDisconnect, &authority)
}

func (s *Grants) requireAuthorization(ctx context.Context, g mcpcmd.Grant, secrets GrantSecrets) error {
	updated := g
	updated.Generation++
	updated.Status = mcpcmd.GrantAuthRequired
	updated.AccessExpiresAt = time.Time{}
	updated.UpdatedAt = time.Now().UTC()
	secrets.AccessToken, secrets.RefreshToken, secrets.TokenType = "", "", ""
	if err := s.save(ctx, g, updated, secrets, mcpcmd.GrantRenew, nil); err != nil {
		return err
	}
	return mcpcmd.ErrAuthRequired
}

func (s *Grants) save(ctx context.Context, previous, updated mcpcmd.Grant, secrets GrantSecrets, operation mcpcmd.GrantOperation, authority *mcpcmd.Authority) error {
	payload, err := s.credentials.ProtectGrant(updated, secrets)
	if err != nil {
		return err
	}
	updated.ProtectedValues = payload
	audit := usercmd.AuditEvent{ID: rand.Text(), Action: usercmd.AuditActionMCPAuthorizationChanged, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetMCP, TargetID: updated.Binding.ConnectionID, Source: "mcp-management", OccurredAt: updated.UpdatedAt}
	if authority == nil {
		audit.Action = usercmd.AuditActionMCPCredentialsRenewed
	} else {
		audit.ActorUserID, audit.ActorSessionID = authority.UserID, authority.SessionID
	}
	return safeOperationError(s.store.SaveGrant(ctx, GrantMutation{Grant: updated, ExpectedGeneration: previous.Generation, Operation: operation, Authority: authority, Audit: audit}))
}

func matchesGrantMetadata(g mcpcmd.Grant, metadata OAuthMetadata) bool {
	return metadata.Resource == g.Binding.Resource && metadata.Issuer == g.Binding.Issuer && validRemoteURL(metadata.TokenEndpoint) && includesScopes(metadata.Scopes, g.Scopes) && (len(metadata.AuthMethods) == 0 || slices.Contains(metadata.AuthMethods, g.TokenEndpointAuthMethod))
}

func includesScopes(supported, required []string) bool {
	if supported == nil {
		return true
	} // An omitted supported list is not a denial.
	return containsScopes(supported, required)
}

func containsScopes(granted, required []string) bool {
	for _, scope := range required {
		if !slices.Contains(granted, scope) {
			return false
		}
	}
	return true
}

// PrepareAuthorization discovers a trusted revision and registers/reuses its
// installation client. Browser/device completion owns later attempt validation.
func (s *Grants) PrepareAuthorization(ctx context.Context, revision mcpcmd.Revision, metadataURL, redirectURI string, client OAuthClient, authority mcpcmd.Authority) (mcpcmd.Grant, OAuthMetadata, error) {
	return s.prepareAuthorization(ctx, revision, metadataURL, redirectURI, client, authority, false)
}

func (s *Grants) prepareAuthorization(ctx context.Context, revision mcpcmd.Revision, metadataURL, redirectURI string, client OAuthClient, authority mcpcmd.Authority, browser bool) (mcpcmd.Grant, OAuthMetadata, error) {
	lock := s.managementLock(revision.ConnectionID)
	lock.Lock()
	defer lock.Unlock()
	authority.At = time.Now().UTC()
	if err := s.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.Grant{}, OAuthMetadata{}, safeOperationError(err)
	}
	if s.credentials.credentials == nil {
		return mcpcmd.Grant{}, OAuthMetadata{}, mcpcmd.ErrCredentials
	}
	d := revision.Definition
	if revision.ConnectionID == "" || revision.ID == "" || !d.OAuth || d.Transport == mcpcmd.TransportStdio || !validRemoteURL(d.URL) {
		return mcpcmd.Grant{}, OAuthMetadata{}, mcpcmd.ErrInvalid
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	metadata, err := s.oauth.Discover(discoveryCtx, d.URL, metadataURL)
	if err != nil {
		return mcpcmd.Grant{}, OAuthMetadata{}, safeOperationError(err)
	}
	if metadata.Resource != d.URL || !validRemoteURL(metadata.Issuer) || !validRemoteURL(metadata.TokenEndpoint) || !includesScopes(metadata.Scopes, d.Scopes) {
		return mcpcmd.Grant{}, OAuthMetadata{}, mcpcmd.ErrInvalid
	}
	if browser && !supportsBrowserAuthorization(metadata) {
		return mcpcmd.Grant{}, OAuthMetadata{}, mcpcmd.ErrUnavailable
	}
	if client.ID == "" {
		client, err = s.oauth.Register(discoveryCtx, metadata, redirectURI, d.Scopes)
		if err != nil {
			return mcpcmd.Grant{}, OAuthMetadata{}, safeOperationError(err)
		}
	}
	authority.At = time.Now().UTC()
	if !validOAuthClient(client, metadata, authority.At) {
		return mcpcmd.Grant{}, OAuthMetadata{}, mcpcmd.ErrInvalid
	}
	binding := mcpcmd.AuthBinding{ConnectionID: revision.ConnectionID, Resource: metadata.Resource, Issuer: metadata.Issuer, ClientID: client.ID}
	previous, found, err := s.store.GetMCPGrant(ctx, binding)
	if err != nil {
		return mcpcmd.Grant{}, OAuthMetadata{}, safeOperationError(err)
	}
	if !found {
		previous = mcpcmd.Grant{ID: rand.Text(), Binding: binding, CreatedAt: authority.At}
	}
	g := previous
	g.Generation++
	g.Status, g.Scopes, g.TokenEndpointAuthMethod = mcpcmd.GrantAuthRequired, slices.Clone(d.Scopes), client.AuthMethod
	g.ClientSecretExpiresAt, g.AccessExpiresAt, g.UpdatedAt = client.SecretExpiresAt, time.Time{}, authority.At
	if err := s.save(ctx, previous, g, GrantSecrets{ClientSecret: client.Secret}, mcpcmd.GrantRegister, &authority); err != nil {
		return mcpcmd.Grant{}, OAuthMetadata{}, err
	}
	// Return public metadata only; the private payload remains behind the store.
	g.ProtectedValues = nil
	return g, metadata, nil
}

// DisconnectConnection revokes all retained resource/client contexts so active
// historical pins cannot keep credentials after a connection-level disconnect.
func (s *Grants) DisconnectConnection(ctx context.Context, connectionID string, authority mcpcmd.Authority) error {
	if connectionID == "" {
		return mcpcmd.ErrInvalid
	}
	lock := s.managementLock(connectionID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.CheckMCPAuthority(ctx, authority); err != nil {
		return safeOperationError(err)
	}
	grants, err := s.store.ListMCPGrants(ctx)
	if err != nil {
		return safeOperationError(err)
	}
	for _, grant := range grants {
		if grant.Binding.ConnectionID != connectionID || grant.Status == mcpcmd.GrantDisconnected {
			continue
		}
		// A refresh may commit between list and disconnect. Re-read that exact
		// context on a CAS conflict; never wait for its external refresh lock.
		for attempt := range 3 {
			authority.At = time.Now().UTC()
			err = s.Disconnect(ctx, grant.Binding, grant.Generation, authority)
			if err == nil {
				break
			}
			if !errors.Is(err, mcpcmd.ErrConflict) || attempt == 2 {
				return err
			}
			var found bool
			grant, found, err = s.store.GetMCPGrant(ctx, grant.Binding)
			if err != nil {
				return safeOperationError(err)
			}
			if !found {
				return mcpcmd.ErrConflict
			}
		}
	}
	return nil
}

func (s *Grants) managementLock(connectionID string) *sync.Mutex {
	lock, _ := s.managementLocks.LoadOrStore(connectionID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func validOAuthClient(client OAuthClient, metadata OAuthMetadata, now time.Time) bool {
	if client.ID == "" || len(client.ID) > 1024 || strings.ContainsAny(client.ID+client.Secret, "\x00\r\n") || len(client.Secret) > 16<<10 || (!client.SecretExpiresAt.IsZero() && !client.SecretExpiresAt.After(now)) {
		return false
	}
	if len(metadata.AuthMethods) > 0 && !slices.Contains(metadata.AuthMethods, client.AuthMethod) {
		return false
	}
	switch client.AuthMethod {
	case mcpcmd.ClientAuthNone:
		return client.Secret == ""
	case mcpcmd.ClientAuthSecretBasic, mcpcmd.ClientAuthSecretPost:
		return client.Secret != ""
	default:
		return false
	}
}
