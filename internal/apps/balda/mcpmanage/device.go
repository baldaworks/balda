package mcpmanage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// OAuthDevice is private protocol state for a single device attempt.
type OAuthDevice struct {
	Code                    string `json:"-"`
	UserCode                string `json:"-"`
	VerificationURI         string `json:"-"`
	VerificationURIComplete string `json:"-"`
	ExpiresAt               time.Time
	Interval                int64
}

// BeginDevice starts lifecycle-owned polling for a supported service.
func (s *Authorizations) BeginDevice(ctx context.Context, r mcpcmd.Revision, metadataURL string, client OAuthClient, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error) {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.DeviceAuthorization{}, safeOperationError(err)
	}
	if r.ConnectionID == "" {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrInvalid
	}
	a, err := s.reserve(r.ConnectionID, authority, authorizationDevice)
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, err
	}
	started := false
	defer func() {
		if !started {
			s.finish(a)
		}
	}()
	ctx, stop := bindAttemptContext(ctx, a.ctx)
	defer stop()
	g, metadata, err := s.grants.prepareAuthorization(ctx, r, metadataURL, "", client, authority, authorizationDevice)
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, err
	}
	stored, found, err := s.grants.store.GetMCPGrant(ctx, g.Binding)
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, safeOperationError(err)
	}
	if !found || stored.Generation != g.Generation || stored.Status != mcpcmd.GrantAuthRequired {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrConflict
	}
	g = stored
	secrets, err := s.grants.credentials.OpenGrant(g)
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, err
	}
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.DeviceAuthorization{}, safeOperationError(err)
	}
	if !validOAuthClient(OAuthClient{ID: g.Binding.ClientID, Secret: secrets.ClientSecret, AuthMethod: g.TokenEndpointAuthMethod, SecretExpiresAt: g.ClientSecretExpiresAt}, metadata, authority.At) {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrAuthRequired
	}
	beginCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	device, err := s.grants.oauth.BeginDevice(beginCtx, metadata, g, secrets)
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, safeOperationError(err)
	}
	if !validOAuthDevice(device, time.Now()) {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts[a.ID] != a || a.ctx.Err() != nil {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrAuthAttempt
	}
	if device.ExpiresAt.Before(a.ExpiresAt) {
		a.ExpiresAt = device.ExpiresAt
	}
	device.ExpiresAt = a.ExpiresAt
	a.Grant, a.Metadata, a.Ready, a.RevisionID = g, metadata, true, r.ID
	a.Device = mcpcmd.DeviceAuthorization{ID: a.ID, ConnectionID: a.ConnectionID, VerificationURI: device.VerificationURI, VerificationURIComplete: device.VerificationURIComplete, UserCode: device.UserCode, ExpiresAt: a.ExpiresAt, Status: mcpcmd.DevicePending}
	s.polls.Add(1)
	go s.pollDevice(a, device, secrets)
	started = true
	return a.Device, nil
}

// Device reads current browser-bound device instructions and safe progress.
func (s *Authorizations) Device(ctx context.Context, id string, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error) {
	authority.At = time.Now().UTC()
	if err := s.grants.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.DeviceAuthorization{}, safeOperationError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	a := s.attempts[id]
	if a == nil || a.Flow != authorizationDevice || !a.Ready {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrAuthAttempt
	}
	if !sameBrowserAuthority(a.Authority, authority) {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrForbidden
	}
	return a.Device, nil
}

func (s *Authorizations) pollDevice(a *authorizationAttempt, device OAuthDevice, secrets GrantSecrets) {
	defer s.polls.Done()
	ctx, cancel := context.WithDeadline(a.ctx, device.ExpiresAt)
	defer cancel()
	token, err := s.grants.oauth.PollDevice(ctx, a.Metadata, a.Grant, secrets, device)
	s.mu.Lock()
	if s.attempts[a.ID] != a {
		s.mu.Unlock()
		return
	}
	if ctx.Err() != nil || !a.ExpiresAt.After(time.Now()) {
		err = mcpcmd.ErrAuthAttempt
	}
	if err == nil {
		err = s.installDevice(ctx, a, secrets, token)
	}
	a.Device.Status = mcpcmd.DeviceAuthorized
	switch {
	case errors.Is(err, mcpcmd.ErrAuthRequired):
		a.Device.Status = mcpcmd.DeviceDenied
	case errors.Is(err, mcpcmd.ErrAuthAttempt):
		a.Device.Status = mcpcmd.DeviceExpired
	case err != nil:
		a.Device.Status = mcpcmd.DeviceFailed
	}
	a.Device.UserCode, a.Device.VerificationURI, a.Device.VerificationURIComplete = "", "", ""
	a.Consumed = true
	if s.byConnection[a.ConnectionID] == a.ID {
		delete(s.byConnection, a.ConnectionID)
	}
	a.cancel()
	snapshot := *a
	s.mu.Unlock()
	if err == nil {
		// Authorized describes the saved grant, even when attachment fails.
		// The catalog/definition read exposes readiness for explicit retry.
		_ = s.bindAuthorization(s.ctx, snapshot, snapshot.Authority)
	}
}

// Caller holds the attempt mutex so cancellation and durable completion agree.
func (s *Authorizations) installDevice(ctx context.Context, a *authorizationAttempt, secrets GrantSecrets, token OAuthToken) error {
	g, found, err := s.grants.store.GetMCPGrant(ctx, a.Grant.Binding)
	if err != nil {
		return safeOperationError(err)
	}
	if !found || g.Generation != a.Grant.Generation || g.Status != mcpcmd.GrantAuthRequired {
		return mcpcmd.ErrConflict
	}
	if !validOAuthClient(OAuthClient{ID: g.Binding.ClientID, Secret: secrets.ClientSecret, AuthMethod: g.TokenEndpointAuthMethod, SecretExpiresAt: g.ClientSecretExpiresAt}, a.Metadata, time.Now()) {
		return mcpcmd.ErrAuthRequired
	}
	token.Secrets.ClientSecret = secrets.ClientSecret
	updated := g
	updated.Generation++
	updated.Status, updated.AccessExpiresAt = mcpcmd.GrantAuthorized, token.ExpiresAt
	if !validGrantSecrets(updated, token.Secrets) || (!token.ExpiresAt.IsZero() && !token.ExpiresAt.After(time.Now())) || (token.Scopes != nil && !containsScopes(token.Scopes, g.Scopes)) {
		return mcpcmd.ErrAuthRequired
	}
	authority := a.Authority
	authority.At = time.Now().UTC()
	updated.UpdatedAt = authority.At
	return s.grants.save(ctx, g, updated, token.Secrets, mcpcmd.GrantAuthorize, &authority, a.RevisionID)
}

func supportsDeviceAuthorization(metadata OAuthMetadata) bool {
	return validRemoteURL(metadata.DeviceAuthorizationEndpoint) && slices.Contains(metadata.GrantTypes, deviceGrantType)
}

func validOAuthDevice(device OAuthDevice, now time.Time) bool {
	return device.Code != "" && len(device.Code) <= 16<<10 && !strings.ContainsAny(device.Code, "\x00\r\n") && device.UserCode != "" && len(device.UserCode) <= 256 && !strings.ContainsAny(device.UserCode, "\x00\r\n") && validRemoteURL(device.VerificationURI) && (device.VerificationURIComplete == "" || validRemoteURL(device.VerificationURIComplete)) && device.ExpiresAt.After(now) && device.Interval >= 0 && device.Interval <= int64(authorizationAttemptTTL/time.Second)
}
