package mcpmanage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

const deviceShutdownCase = "shutdown"

func waitDeviceStatus(t *testing.T, s *Authorizations, id string, authority mcpcmd.Authority, want mcpcmd.DeviceStatus) mcpcmd.DeviceAuthorization {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		status, err := s.Device(t.Context(), id, authority)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == want {
			return status
		}
		select {
		case <-deadline.C:
			t.Fatalf("device status = %s, want %s", status.Status, want)
		case <-tick.C:
		}
	}
}

func deviceHarness(t *testing.T) (*Authorizations, *grantMemoryStore, *grantOAuth, mcpcmd.Revision, mcpcmd.Authority) {
	t.Helper()
	s, store, o, r, a := browserHarness(t)
	o.metadata.DeviceAuthorizationEndpoint = o.metadata.Issuer + "/device"
	o.metadata.GrantTypes = []string{deviceGrantType}
	o.metadata.AuthorizationEndpoint = ""
	o.metadata.PKCEMethods = nil
	o.metadata.RequireIssuerParameter = false
	o.device = OAuthDevice{Code: "private-device-code", UserCode: "ABCD-EFGH", VerificationURI: o.metadata.Issuer + "/verify", ExpiresAt: time.Now().Add(time.Minute), Interval: 1}
	return s, store, o, r, a
}

func TestDeviceDefinitionEditDuringPollCannotInstallGrant(t *testing.T) {
	s, store, o, r, a := deviceHarness(t)
	defer s.Close()
	o.poll = func(context.Context, mcpcmd.Grant, GrantSecrets, OAuthDevice) (OAuthToken, error) {
		store.mu.Lock()
		store.currentRevisionID = "edited-revision"
		store.mu.Unlock()
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "stale-device-token", TokenType: "Bearer"}}, nil
	}
	started, err := s.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	status := waitDeviceStatus(t, s, started.ID, a, mcpcmd.DeviceFailed)
	store.mu.Lock()
	defer store.mu.Unlock()
	if status.Status != mcpcmd.DeviceFailed || store.grant.Status != mcpcmd.GrantAuthRequired || len(store.writes) != 1 {
		t.Fatal("stale device poll installed a grant")
	}
}

func TestDeviceCompletionBindsSavedGrantOutsideAttemptLock(t *testing.T) {
	s, _, o, r, a := deviceHarness(t)
	defer s.Close()
	bound := make(chan mcpcmd.SelectAuthorization, 1)
	s.definitions = authorizationBinder{bind: func(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
		if !s.mu.TryLock() {
			t.Error("binding held the attempt lock")
			return mcpcmd.Item{}, mcpcmd.ErrUnavailable
		}
		s.mu.Unlock()
		if _, err := s.grants.RequestCredentials(ctx, request.Binding, r.Definition.Scopes); err != nil {
			t.Errorf("binding cannot acquire saved grant: %v", err)
		}
		bound <- request
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}}
	o.poll = func(context.Context, mcpcmd.Grant, GrantSecrets, OAuthDevice) (OAuthToken, error) {
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "device-token", TokenType: "Bearer"}}, nil
	}
	started, err := s.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-bound:
		if request.ExpectedRevisionID != r.ID || request.ConnectionID != r.ConnectionID {
			t.Fatal("device completion selected another revision")
		}
	case <-time.After(time.Second):
		t.Fatal("saved device grant was not bound")
	}
	_ = waitDeviceStatus(t, s, started.ID, a, mcpcmd.DeviceAuthorized)
}

func TestDeviceAuthorizationUsesPersistedConfidentialClientSecret(t *testing.T) {
	s, _, o, r, a := deviceHarness(t)
	defer s.Close()
	o.metadata.AuthMethods = []string{mcpcmd.ClientAuthSecretBasic}
	o.deviceBegin = func(_ context.Context, _ mcpcmd.Grant, secrets GrantSecrets) (OAuthDevice, error) {
		if secrets.ClientSecret != "worker-client-secret" {
			return OAuthDevice{}, mcpcmd.ErrCredentials
		}
		return o.device, nil
	}
	o.poll = func(ctx context.Context, _ mcpcmd.Grant, secrets GrantSecrets, _ OAuthDevice) (OAuthToken, error) {
		if secrets.ClientSecret != "worker-client-secret" {
			t.Error("polling lost confidential client secret")
		}
		<-ctx.Done()
		return OAuthToken{}, ctx.Err()
	}
	if _, err := s.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", Secret: "worker-client-secret", AuthMethod: mcpcmd.ClientAuthSecretBasic}, a); err != nil {
		t.Fatalf("confidential device authorization failed: %v", err)
	}
}

type deviceAuthorityLoadStore struct {
	*browserAuthorityExpiryStore
	loads int
}

func (s *deviceAuthorityLoadStore) GetMCPGrant(ctx context.Context, binding mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error) {
	g, found, err := s.grantMemoryStore.GetMCPGrant(ctx, binding)
	s.loads++
	if s.loads == 2 {
		s.expires = time.Now().UTC()
	}
	return g, found, err
}

func TestDeviceBeginRechecksAuthorityAfterGrantLoadBeforeNetwork(t *testing.T) {
	s, memory, o, r, a := deviceHarness(t)
	defer s.Close()
	s.grants.store = &deviceAuthorityLoadStore{browserAuthorityExpiryStore: &browserAuthorityExpiryStore{grantMemoryStore: memory, expires: time.Now().Add(time.Hour)}}
	requested := false
	o.deviceBegin = func(context.Context, mcpcmd.Grant, GrantSecrets) (OAuthDevice, error) {
		requested = true
		return o.device, nil
	}
	o.poll = func(ctx context.Context, _ mcpcmd.Grant, _ GrantSecrets, _ OAuthDevice) (OAuthToken, error) {
		<-ctx.Done()
		return OAuthToken{}, ctx.Err()
	}
	_, err := s.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if !errors.Is(err, mcpcmd.ErrForbidden) || requested {
		t.Fatalf("expired browser authority dispatched device request: %v", err)
	}
}

func TestDeviceAuthorizationInstallsWorkerGrantWithoutCallback(t *testing.T) {
	s, _, o, r, a := deviceHarness(t)
	defer s.Close()
	polled, release := make(chan struct{}), make(chan struct{})
	o.poll = func(ctx context.Context, _ mcpcmd.Grant, _ GrantSecrets, device OAuthDevice) (OAuthToken, error) {
		if device.Code != "private-device-code" {
			t.Error("private device binding lost")
		}
		close(polled)
		select {
		case <-release:
		case <-ctx.Done():
			return OAuthToken{}, ctx.Err()
		}
		return OAuthToken{Secrets: GrantSecrets{AccessToken: "device-worker-access", RefreshToken: "device-worker-refresh", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"tools"}}, nil
	}
	started, err := s.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a)
	if err != nil {
		t.Fatalf("supported device authorization did not begin: %v", err)
	}
	if started.Status != mcpcmd.DevicePending || started.UserCode != o.device.UserCode || started.VerificationURI != o.device.VerificationURI {
		t.Fatal("device instructions unavailable")
	}
	data, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{o.device.Code, o.device.UserCode, o.device.VerificationURI} {
		if strings.Contains(string(data), secret) {
			t.Fatal("device protocol material entered ordinary projection")
		}
	}
	select {
	case <-polled:
	case <-time.After(time.Second):
		t.Fatal("lifecycle polling did not start")
	}
	close(release)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := s.Device(t.Context(), started.ID, a)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == mcpcmd.DeviceAuthorized {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("authorization did not complete: %s", status.Status)
		}
	}
	binding := mcpcmd.AuthBinding{ConnectionID: r.ConnectionID, Resource: o.metadata.Resource, Issuer: o.metadata.Issuer, ClientID: "worker-client"}
	credentials, err := s.grants.RequestCredentials(t.Context(), binding, []string{"tools"})
	if err != nil || credentials.AccessToken != "device-worker-access" {
		t.Fatalf("completed device grant unavailable: %v", err)
	}
}

func TestDeviceAuthorizationDoesNotRequireBrowserCallbackConfiguration(t *testing.T) {
	s, _, o, r, a := deviceHarness(t)
	defer s.Close()
	deviceOnly, err := NewAuthorizations(s.grants, "", s.definitions)
	if err != nil {
		t.Fatalf("device-only service requires browser callback: %v", err)
	}
	defer deviceOnly.Close()
	o.poll = func(ctx context.Context, _ mcpcmd.Grant, _ GrantSecrets, _ OAuthDevice) (OAuthToken, error) {
		<-ctx.Done()
		return OAuthToken{}, ctx.Err()
	}
	if _, err := deviceOnly.BeginDevice(t.Context(), r, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, a); err != nil {
		t.Fatalf("device-only begin failed: %v", err)
	}
}

func TestDeviceUnsupportedCapabilityAndMissingClientPreserveGrant(t *testing.T) {
	for _, change := range []func(*grantOAuth, *OAuthClient){
		func(o *grantOAuth, _ *OAuthClient) { o.metadata.DeviceAuthorizationEndpoint = "" },
		func(o *grantOAuth, _ *OAuthClient) { o.metadata.GrantTypes = []string{"authorization_code"} },
		func(_ *grantOAuth, c *OAuthClient) { c.ID = "" },
	} {
		s, store, o, r, a := deviceHarness(t)
		client := OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}
		change(o, &client)
		before := store.grant
		o.register = func(context.Context) (OAuthClient, error) {
			t.Error("unsupported device begin registered another client")
			return OAuthClient{}, mcpcmd.ErrUnavailable
		}
		_, err := s.BeginDevice(t.Context(), r, "", client, a)
		s.Close()
		if !errors.Is(err, mcpcmd.ErrUnavailable) || len(store.writes) != 0 || store.grant.Generation != before.Generation || store.grant.Status != before.Status {
			t.Fatalf("unsupported begin changed worker grant: %v", err)
		}
	}
}

func TestDeviceLatePollingCannotInstallAfterCancelDisconnectRestartOrGenerationChange(t *testing.T) {
	for _, action := range []string{"cancel", "disconnect", deviceShutdownCase, "generation", "expire", "authority"} {
		t.Run(action, func(t *testing.T) {
			s, memory, o, r, a := deviceHarness(t)
			defer s.Close()
			store := &browserAuthorityExpiryStore{grantMemoryStore: memory, expires: time.Now().Add(time.Hour)}
			s.grants.store = store
			if action == "expire" {
				o.device.ExpiresAt = time.Now().Add(200 * time.Millisecond)
			}
			polled, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			o.poll = func(ctx context.Context, _ mcpcmd.Grant, _ GrantSecrets, _ OAuthDevice) (OAuthToken, error) {
				close(polled)
				if action == deviceShutdownCase {
					<-ctx.Done()
					close(cancelled)
				}
				<-release
				return OAuthToken{Secrets: GrantSecrets{AccessToken: "late-device-token", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
			}
			client := OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}
			started, err := s.BeginDevice(t.Context(), r, "", client, a)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-polled:
			case <-time.After(time.Second):
				t.Fatal("poll did not start")
			}
			switch action {
			case "cancel":
				err = s.Cancel(t.Context(), started.ID, a)
			case "disconnect":
				err = s.Disconnect(t.Context(), r.ConnectionID, a)
			case "generation":
				_, _, err = s.grants.PrepareAuthorization(t.Context(), r, "", "", client, a)
			case "authority":
				store.expires = time.Now().UTC()
			case "expire":
				timer := time.NewTimer(time.Until(o.device.ExpiresAt.Add(time.Millisecond)))
				<-timer.C
			case deviceShutdownCase:
				closed := make(chan struct{})
				go func() { s.Close(); close(closed) }()
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("shutdown did not cancel polling")
				}
				close(release)
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("shutdown did not join polling")
				}
			}
			if action != deviceShutdownCase {
				close(release)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.polls.Wait()
			g, found, err := memory.GetMCPGrant(t.Context(), mcpcmd.AuthBinding{ConnectionID: r.ConnectionID, Resource: o.metadata.Resource, Issuer: o.metadata.Issuer, ClientID: client.ID})
			if err != nil || !found || g.Status == mcpcmd.GrantAuthorized {
				t.Fatalf("late polling authorized worker after %s: %v", action, err)
			}
			if action == deviceShutdownCase {
				restarted, err := NewAuthorizations(s.grants, "", s.definitions)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				if _, err := restarted.Device(t.Context(), started.ID, a); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
					t.Fatal("unfinished device attempt survived restart")
				}
				o.poll = func(context.Context, mcpcmd.Grant, GrantSecrets, OAuthDevice) (OAuthToken, error) {
					return OAuthToken{Secrets: GrantSecrets{AccessToken: "retried-device-token", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
				}
				if _, err := restarted.BeginDevice(t.Context(), r, "", client, a); err != nil {
					t.Fatal(err)
				}
				restarted.polls.Wait()
				credentials, err := restarted.grants.RequestCredentials(t.Context(), g.Binding, []string{"tools"})
				if err != nil || credentials.AccessToken != "retried-device-token" {
					t.Fatal("new device attempt could not complete after restart")
				}
			}
		})
	}
}

func TestDeviceDenialIsTerminalAndOneConnectionSharesBrowserBudget(t *testing.T) {
	s, store, o, r, a := deviceHarness(t)
	defer s.Close()
	polled, release := make(chan struct{}), make(chan struct{})
	o.poll = func(context.Context, mcpcmd.Grant, GrantSecrets, OAuthDevice) (OAuthToken, error) {
		close(polled)
		<-release
		return OAuthToken{}, mcpcmd.ErrAuthRequired
	}
	client := OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}
	started, err := s.BeginDevice(t.Context(), r, "", client, a)
	if err != nil {
		t.Fatal(err)
	}
	<-polled
	if _, err := s.BeginBrowser(t.Context(), r, "", client, a); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("browser bypassed active device attempt: %v", err)
	}
	wrong := a
	wrong.SessionID = "another-browser"
	if _, err := s.Device(t.Context(), started.ID, wrong); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatal("device instructions crossed browser boundary")
	}
	if _, err := s.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: started.ID, Code: "one-use-code", Issuer: o.metadata.Issuer, Authority: a}); !errors.Is(err, mcpcmd.ErrAuthAttempt) {
		t.Fatal("device ID accepted as browser protocol state")
	}
	close(release)
	s.polls.Wait()
	status, err := s.Device(t.Context(), started.ID, a)
	if err != nil || status.Status != mcpcmd.DeviceDenied || status.UserCode != "" || store.grant.Status != mcpcmd.GrantAuthRequired {
		t.Fatalf("denied polling installed grant or retained instructions: %v", err)
	}
}
