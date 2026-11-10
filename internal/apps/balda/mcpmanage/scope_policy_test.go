package mcpmanage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestBrowserScopeSelectionPreservesResourceIntentAndRenewalCapabilities(t *testing.T) {
	allScopes := []string{"openid", "profile", "email", offlineAccessScope}
	refresh := []string{"authorization_code", "refresh_token"}
	many := make([]string, 64)
	for i := range many {
		many[i] = fmt.Sprintf("resource:%d", i)
	}
	for _, tc := range []struct {
		name                        string
		requested, resource, server []string
		grants, want                []string
		invalid                     bool
	}{
		{"resource defaults", nil, []string{"openid"}, allScopes, refresh, []string{"openid", offlineAccessScope}, false},
		{"explicit narrowing", []string{"tools:read"}, []string{"tools:read", "tools:write"}, []string{"tools:read", "tools:write", offlineAccessScope}, refresh, []string{"tools:read", offlineAccessScope}, false},
		{"protocol-only selection", []string{offlineAccessScope}, []string{"openid"}, allScopes, refresh, []string{"openid", offlineAccessScope}, false},
		{"no refresh grant", []string{offlineAccessScope}, []string{"openid"}, allScopes, []string{"authorization_code"}, []string{"openid"}, false},
		{"no offline permission", nil, []string{"openid"}, []string{"openid"}, refresh, []string{"openid"}, false},
		{"unknown AS scopes", nil, []string{"openid"}, nil, refresh, []string{"openid"}, false},
		{"unknown grant capabilities", nil, []string{"openid"}, allScopes, nil, []string{"openid"}, false},
		{"omitted resource defaults", nil, nil, allScopes, refresh, []string{offlineAccessScope}, false},
		{"no scope advertised", nil, nil, nil, nil, nil, false},
		{"empty resource defaults", nil, []string{}, allScopes, refresh, []string{offlineAccessScope}, false},
		{"explicit with omitted PRM scopes", []string{"openid"}, nil, allScopes, refresh, []string{"openid", offlineAccessScope}, false},
		{"explicit against empty PRM scopes", []string{"openid"}, []string{}, allScopes, refresh, nil, true},
		{"contradictory AS metadata", nil, []string{"openid"}, []string{offlineAccessScope}, refresh, nil, true},
		{"empty AS capabilities", nil, []string{"openid"}, []string{}, refresh, nil, true},
		{"deduplicated resource defaults", nil, []string{"openid", "openid", offlineAccessScope}, allScopes, refresh, []string{"openid", offlineAccessScope}, false},
		{"deduplicated explicit choice", []string{"openid", offlineAccessScope, "openid"}, []string{"openid"}, allScopes, refresh, []string{"openid", offlineAccessScope}, false},
		{"invalid explicit token", []string{"openid email"}, []string{"openid"}, allScopes, refresh, nil, true},
		{"invalid resource default", nil, []string{"openid\n"}, allScopes, refresh, nil, true},
		{"oversized token", []string{strings.Repeat("s", 257)}, nil, nil, nil, nil, true},
		{"too many request scopes", append(slices.Clone(many), "extra"), nil, nil, nil, nil, true},
		{"too many resource defaults", nil, append(slices.Clone(many), "extra"), nil, nil, nil, true},
		{"renewal exceeds scope bound", many, many, append(slices.Clone(many), offlineAccessScope), refresh, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flows, store, oauth, revision, authority := browserHarness(t)
			defer flows.Close()
			revision.Definition.Scopes = slices.Clone(tc.requested)
			oauth.metadata.ResourceScopes, oauth.metadata.Scopes = tc.resource, tc.server
			oauth.metadata.GrantTypes = tc.grants
			writes := len(store.writes)
			_, err := flows.BeginBrowser(t.Context(), revision, "", OAuthClient{}, authority)
			if tc.invalid {
				if !errors.Is(err, mcpcmd.ErrInvalid) || oauth.registrationRedirect != "" || len(store.writes) != writes {
					t.Fatalf("invalid scope selection registered or saved: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(store.grant.Scopes, tc.want) || !slices.Equal(oauth.registrationScopes, tc.want) || !slices.Equal(oauth.authorizationScopes, tc.want) {
				t.Fatalf("scopes registration=%v begin=%v grant=%v, want %v", oauth.registrationScopes, oauth.authorizationScopes, store.grant.Scopes, tc.want)
			}
			if !slices.Equal(revision.Definition.Scopes, tc.requested) {
				t.Fatal("derived scopes rewrote definition intent")
			}
		})
	}
}

func TestBrowserAndDeviceRetainActualResourceConsent(t *testing.T) {
	for _, device := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			response []string
			want     []string
			denied   bool
		}{
			{"omitted scope", nil, []string{"openid", offlineAccessScope}, false},
			{"optional renewal refused", []string{"openid"}, []string{"openid"}, false},
			{"extra permissions ignored", []string{"openid", "email", offlineAccessScope}, []string{"openid", offlineAccessScope}, false},
			{"resource missing", []string{offlineAccessScope}, nil, true},
			{"empty explicit scope", []string{}, nil, true},
			{"malformed scope", []string{"openid", "bad\\scope"}, nil, true},
		} {
			t.Run(fmt.Sprintf("%s/device=%v", tc.name, device), func(t *testing.T) {
				flows, store, oauth, revision, authority := browserHarness(t)
				defer flows.Close()
				revision.Definition.Scopes = []string{offlineAccessScope}
				oauth.metadata.ResourceScopes = []string{"openid"}
				oauth.metadata.Scopes = []string{"openid", offlineAccessScope}
				oauth.metadata.GrantTypes = []string{"authorization_code", "refresh_token", deviceGrantType}
				token := OAuthToken{Secrets: GrantSecrets{AccessToken: "resource-access", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(10 * time.Second), Scopes: tc.response}
				var grant mcpcmd.Grant
				var err error
				if device {
					oauth.metadata.DeviceAuthorizationEndpoint = oauth.metadata.Issuer + "/device"
					oauth.device = OAuthDevice{Code: "private-code", UserCode: "CODE", VerificationURI: oauth.metadata.Issuer + "/verify", ExpiresAt: time.Now().Add(time.Minute)}
					oauth.deviceBegin = func(_ context.Context, g mcpcmd.Grant, _ GrantSecrets) (OAuthDevice, error) {
						if !slices.Equal(g.Scopes, []string{"openid", offlineAccessScope}) {
							t.Error("device start lost effective scope")
						}
						return oauth.device, nil
					}
					oauth.poll = func(_ context.Context, g mcpcmd.Grant, _ GrantSecrets, _ OAuthDevice) (OAuthToken, error) {
						if !slices.Equal(g.Scopes, []string{"openid", offlineAccessScope}) {
							t.Error("device exchange lost effective scope")
						}
						return token, nil
					}
					started, beginErr := flows.BeginDevice(t.Context(), revision, "", OAuthClient{ID: "worker-client", AuthMethod: mcpcmd.ClientAuthNone}, authority)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					wantStatus := mcpcmd.DeviceAuthorized
					if tc.denied {
						wantStatus = mcpcmd.DeviceDenied
					}
					waitDeviceStatus(t, flows, started.ID, authority, wantStatus)
					store.mu.Lock()
					grant = store.grant
					store.mu.Unlock()
				} else {
					oauth.exchange = func(context.Context, mcpcmd.Grant, string, string) (OAuthToken, error) { return token, nil }
					started, beginErr := flows.BeginBrowser(t.Context(), revision, "", OAuthClient{}, authority)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					grant, err = flows.CompleteBrowser(t.Context(), mcpcmd.BrowserCallback{State: browserState(t, started), Code: "code", CallbackPath: "/balda/mcp/oauth/callback", Issuer: oauth.metadata.Issuer, Authority: authority})
					if tc.denied && !errors.Is(err, mcpcmd.ErrAuthRequired) {
						t.Fatalf("insufficient token scope accepted: %v", err)
					}
					if !slices.Equal(oauth.exchangeScopes, []string{"openid", offlineAccessScope}) {
						t.Fatal("code exchange lost effective scope")
					}
				}
				if tc.denied {
					if store.grant.Status != mcpcmd.GrantAuthRequired {
						t.Fatal("insufficient resource authorization installed")
					}
					return
				}
				if err != nil || grant.Status != mcpcmd.GrantAuthorized || !slices.Equal(grant.Scopes, tc.want) {
					t.Fatalf("grant status=%s scopes=%v err=%v, want %v", grant.Status, grant.Scopes, err, tc.want)
				}
				if _, err := flows.grants.RequestCredentials(t.Context(), grant.Binding, revision.Definition.Scopes); err != nil {
					t.Fatalf("valid resource access with protocol-only historical requirement failed: %v", err)
				}
			})
		}
	}
}

func TestRefreshKeepsGrantedScopesWithoutApplyingNewResourceDefaults(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		requested, response, want []string
		rotated                   bool
		denied                    bool
	}{
		{"omitted scope", []string{"tools", offlineAccessScope}, nil, []string{"tools", offlineAccessScope}, false, false},
		{"offline consent omitted", []string{"tools", offlineAccessScope}, []string{"tools"}, []string{"tools"}, true, false},
		{"extra scope ignored", []string{"tools", offlineAccessScope}, []string{"tools", offlineAccessScope, "email"}, []string{"tools", offlineAccessScope}, false, false},
		{"no new offline scope", []string{"tools"}, []string{"tools", offlineAccessScope}, []string{"tools"}, false, false},
		{"required resource denied", []string{"tools", offlineAccessScope}, []string{offlineAccessScope}, nil, false, true},
		{"explicit empty denied", []string{"tools", offlineAccessScope}, []string{}, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grants, store, oauth := grantHarness(t)
			store.grant.Scopes = slices.Clone(tc.requested)
			oauth.metadata.Scopes = []string{"tools", offlineAccessScope, "email"}
			oauth.metadata.ResourceScopes = []string{"new-default"}
			oauth.metadata.GrantTypes = []string{"refresh_token"}
			previous := store.grant
			calls := 0
			oauth.refresh = func(_ context.Context, g mcpcmd.Grant, old GrantSecrets) (OAuthToken, error) {
				calls++
				wantScopes := tc.requested
				if calls > 1 {
					wantScopes = tc.want
				}
				if !slices.Equal(g.Scopes, wantScopes) || g.Binding != previous.Binding {
					t.Error("refresh recomputed scopes or changed the retained binding")
				}
				wantRefresh := "original-refresh"
				if calls > 1 && tc.rotated {
					wantRefresh = "new-refresh"
				}
				if old.RefreshToken != wantRefresh {
					t.Error("refresh rotation identity lost")
				}
				token := OAuthToken{Secrets: GrantSecrets{AccessToken: "new-access", TokenType: "Bearer"}, ExpiresAt: time.Now().Add(time.Hour), Scopes: tc.response}
				if tc.rotated {
					token.Secrets.RefreshToken = "new-refresh"
				}
				return token, nil
			}
			_, err := grants.RequestCredentials(t.Context(), previous.Binding, []string{"tools", offlineAccessScope})
			if tc.denied {
				if !errors.Is(err, mcpcmd.ErrAuthRequired) || store.grant.Status != mcpcmd.GrantAuthRequired {
					t.Fatalf("insufficient refreshed resource scope accepted: %v", err)
				}
				return
			}
			if err != nil || !slices.Equal(store.grant.Scopes, tc.want) || store.grant.Generation != previous.Generation+1 || store.grant.Binding != previous.Binding {
				t.Fatalf("refreshed scopes=%v err=%v, want %v", store.grant.Scopes, err, tc.want)
			}
			// A second renewal must request the actual retained consent, not defaults.
			store.grant.AccessExpiresAt = time.Now().Add(-time.Second)
			if _, err := grants.RequestCredentials(t.Context(), previous.Binding, []string{"tools"}); err != nil || calls != 2 {
				t.Fatalf("subsequent refresh did not retain actual consent: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestOfflinePermissionDoesNotReplaceRequiredResourceAccess(t *testing.T) {
	grants, store, _ := grantHarness(t)
	store.grant.AccessExpiresAt = time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name     string
		required []string
		allowed  bool
	}{
		{"protocol only", []string{offlineAccessScope}, true},
		{"resource and protocol", []string{"tools", offlineAccessScope}, true},
		{"missing resource", []string{"other", offlineAccessScope}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, workerErr := grants.RequestCredentials(t.Context(), store.grant.Binding, tc.required)
			_, diagnosticErr := grants.DiagnosticAccessToken(t.Context(), store.grant.Binding, tc.required)
			if (workerErr == nil) != tc.allowed || (diagnosticErr == nil) != tc.allowed {
				t.Fatalf("resource interpretation differs: worker=%v diagnostic=%v", workerErr, diagnosticErr)
			}
			if !tc.allowed && (!errors.Is(workerErr, mcpcmd.ErrAuthRequired) || !errors.Is(diagnosticErr, mcpcmd.ErrAuthRequired)) {
				t.Fatal("missing resource scope did not fail closed")
			}
		})
	}
}

func TestAuthorizationBindingUsesResourceRequirementsWithoutRewritingIntent(t *testing.T) {
	for _, tc := range []struct {
		name              string
		required, granted []string
		allowed           bool
	}{
		{"protocol only", []string{offlineAccessScope}, []string{"openid"}, true},
		{"resource with protocol", []string{"read", offlineAccessScope}, []string{"read"}, true},
		{"missing resource", []string{"read", offlineAccessScope}, []string{"other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definitions, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition = mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://mcp.example.org/tools", OAuth: true, Scopes: tc.required, Targets: mcpcmd.Targets{All: true}}
			created, err := definitions.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			binding := mcpcmd.AuthBinding{ConnectionID: created.Connection.ID, Resource: request.Definition.URL, Issuer: "https://issuer.example.org", ClientID: "worker-client"}
			store.grants = map[mcpcmd.AuthBinding]mcpcmd.Grant{binding: {Binding: binding, Status: mcpcmd.GrantAuthorized, Scopes: tc.granted}}
			bound, err := definitions.BindAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: created.Connection.ID, ExpectedRevisionID: created.Connection.CurrentRevisionID, Binding: binding, Authority: request.Authority})
			if tc.allowed {
				if err != nil || !slices.Equal(bound.Definition.Scopes, tc.required) || store.revisions[created.Connection.CurrentRevisionID].Definition.AuthBinding != nil {
					t.Fatalf("binding rewrote resource intent or historical revision: %v", err)
				}
			} else if !errors.Is(err, mcpcmd.ErrAuthRequired) {
				t.Fatalf("binding accepted missing resource access: %v", err)
			}
		})
	}
}

func TestNonrenewableWorkerAccessFailsAtActualExpiry(t *testing.T) {
	grants, store, oauth := grantHarness(t)
	var err error
	store.grant.ProtectedValues, err = grants.credentials.ProtectGrant(store.grant, GrantSecrets{AccessToken: "expired-access", TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.RequestCredentials(t.Context(), store.grant.Binding, []string{"tools"}); !errors.Is(err, mcpcmd.ErrAuthRequired) || store.grant.Status != mcpcmd.GrantAuthRequired || oauth.discoveries.Load() != 0 || oauth.refreshes.Load() != 0 {
		t.Fatalf("expired nonrenewable access did not fail closed: %v", err)
	}
}
