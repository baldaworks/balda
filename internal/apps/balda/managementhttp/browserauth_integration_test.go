package managementhttp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientWebAuthLifecycle(t *testing.T) {
	handler, _ := newBrowserAuthTestHandler(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	password := browserAuthTestPassword
	binding, csrf := strings.Repeat("b", 32), strings.Repeat("c", 32)
	login, err := client.LoginWebAuthWithResponse(t.Context(), managementv1.LocalLoginRequest{
		Username: browserAuthTestUsername, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, CSRFToken: &csrf},
	})
	if err != nil || login.JSON200 == nil || login.JSON200.Credentials == nil {
		t.Fatalf("login = %#v, %v", login, err)
	}
	credentials := *login.JSON200.Credentials
	accessEditor := credentialRequestEditor(credentials.AccessToken, "")
	principal, err := client.GetWebAuthPrincipalWithResponse(t.Context(), accessEditor)
	if err != nil || principal.JSON200 == nil || principal.JSON200.Username != browserAuthTestUsername || principal.JSON200.WebAuthSessionID == "" {
		t.Fatalf("principal = %#v, %v", principal, err)
	}
	mfa, err := client.GetWebAuthMFAStatusWithResponse(t.Context(), accessEditor)
	if err != nil || mfa.JSON200 == nil || mfa.JSON200.Enabled || !mfa.JSON200.Available {
		t.Fatalf("MFA status = %#v, %v", mfa, err)
	}
	page, err := client.ListWebAuthSessionsWithResponse(t.Context(), &managementv1.ListWebAuthSessionsParams{}, accessEditor)
	if err != nil || page.JSON200 == nil || len(page.JSON200.Sessions) != 1 || page.JSON200.Sessions[0].WebAuthSessionID != principal.JSON200.WebAuthSessionID {
		t.Fatalf("session page = %#v, %v", page, err)
	}
	refresh, err := client.RefreshWebAuthWithResponse(t.Context(), credentialRequestEditor(credentials.RefreshToken, credentials.CSRFToken))
	if err != nil || refresh.JSON200 == nil || refresh.JSON200.AccessToken == credentials.AccessToken {
		t.Fatalf("refresh = %#v, %v", refresh, err)
	}
	rotated := *refresh.JSON200
	replacement := "replacement battery staple"
	replaced, err := client.ReplaceWebAuthPasswordWithResponse(t.Context(), managementv1.WebAuthPasswordReplaceRequest{
		CurrentPassword: &password, NewPassword: &replacement,
	}, credentialRequestEditor(rotated.AccessToken, rotated.CSRFToken))
	if err != nil || replaced.JSON200 == nil || replaced.JSON200.AccessToken == rotated.AccessToken {
		t.Fatalf("password replacement = %#v, %v", replaced, err)
	}
	replacementCredentials := *replaced.JSON200
	logout, err := client.LogoutWebAuthWithResponse(t.Context(), credentialRequestEditor(replacementCredentials.AccessToken, replacementCredentials.CSRFToken))
	if err != nil || logout.StatusCode() != http.StatusNoContent {
		t.Fatalf("logout = %#v, %v", logout, err)
	}
	revoked, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(replacementCredentials.AccessToken, ""))
	if err != nil || revoked.StatusCode() != http.StatusUnauthorized || revoked.JSON401 == nil {
		t.Fatalf("revoked principal = %#v, %v", revoked, err)
	}
}

func TestBrowserAuthGuardOrderingAndRedaction(t *testing.T) {
	handler, service := newBrowserAuthTestHandler(t)
	credentials, err := service.Login(t.Context(), browserAuthTestUsername, []byte(browserAuthTestPassword))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, target, body string
		operator, access, csrf     string
		want                       int
	}{
		{name: "operator before malformed body", method: http.MethodPost, target: "/api/management/v1/auth/login", body: "{", want: http.StatusUnauthorized},
		{name: "malformed active body", method: http.MethodPost, target: "/api/management/v1/auth/login", body: "{", operator: "operator-secret", want: http.StatusBadRequest},
		{name: "access before generated route", method: http.MethodGet, target: "/api/management/v1/auth/me", operator: "operator-secret", want: http.StatusUnauthorized},
		{name: "AI session ID cannot authorize", method: http.MethodGet, target: "/api/management/v1/auth/me", operator: "operator-secret", access: "same-raw-ai-session-id", want: http.StatusUnauthorized},
		{name: "csrf required", method: http.MethodPost, target: "/api/management/v1/auth/logout", operator: "operator-secret", access: credentials.AccessToken, want: http.StatusForbidden},
		{name: "invalid csrf", method: http.MethodPost, target: "/api/management/v1/auth/logout", operator: "operator-secret", access: credentials.AccessToken, csrf: "credential-canary", want: http.StatusForbidden},
		{name: "unknown route", method: http.MethodPost, target: "/api/management/v1/auth/provider/action", body: "{", operator: "operator-secret", want: http.StatusNotFound},
		{name: "wrong method", method: http.MethodPut, target: "/api/management/v1/auth/login", body: "{", operator: "operator-secret", want: http.StatusNotFound},
		{name: "oversized active body", method: http.MethodPost, target: "/api/management/v1/auth/login", body: strings.Repeat("x", maxManagementBody+1), operator: "operator-secret", want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.operator != "" {
				request.Header.Set(authorizationHeader, "Bearer "+test.operator)
			}
			if test.access != "" {
				request.Header.Set(webAuthAccessHeader, test.access)
			}
			if test.csrf != "" {
				request.Header.Set(csrfHeader, test.csrf)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			for _, secret := range []string{"operator-secret", credentials.AccessToken, credentials.WebAuthRefreshToken, "credential-canary"} {
				if secret != "" && strings.Contains(response.Body.String(), secret) {
					t.Fatalf("response disclosed secret: %s", response.Body.String())
				}
			}
			if response.Header().Get(requestIDHeader) == "" {
				t.Fatal("response omitted request ID")
			}
		})
	}
}

func TestGeneratedClientMFAEnableAndDisable(t *testing.T) {
	handler, _ := newBrowserAuthTestHandler(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	password := browserAuthTestPassword
	binding, csrf := strings.Repeat("b", 32), strings.Repeat("c", 32)
	login, err := client.LoginWebAuthWithResponse(t.Context(), managementv1.LocalLoginRequest{
		Username: browserAuthTestUsername, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, CSRFToken: &csrf},
	})
	if err != nil || login.JSON200 == nil || login.JSON200.Credentials == nil {
		t.Fatalf("login = %#v, %v", login, err)
	}
	credentials := *login.JSON200.Credentials
	accountEditor := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	begin, err := client.BeginWebAuthMFAEnableWithResponse(t.Context(), managementv1.WebAuthMFABeginRequest{Password: &password, BrowserBinding: &binding}, accountEditor)
	if err != nil || begin.JSON200 == nil {
		t.Fatalf("begin MFA enable = %#v, %v", begin, err)
	}
	challenge, ok := publicKeyChallenge(begin.JSON200.PublicKeyOptions)
	if !ok {
		t.Fatalf("MFA options = %#v", begin.JSON200.PublicKeyOptions)
	}
	registration := registrationResponse(t, challenge)
	finish, err := client.FinishWebAuthMFAEnableWithResponse(t.Context(), managementv1.WebAuthMFAFinishRequest{
		TransactionID: begin.JSON200.TransactionID, BrowserBinding: &binding, CredentialResponse: &registration,
	}, accountEditor)
	if err != nil || finish.JSON200 == nil {
		t.Fatalf("finish MFA enable = %#v, %v", finish, err)
	}
	verified := *finish.JSON200
	status, err := client.GetWebAuthMFAStatusWithResponse(t.Context(), credentialRequestEditor(verified.AccessToken, ""))
	if err != nil || status.JSON200 == nil || !status.JSON200.Enabled {
		t.Fatalf("enabled MFA status = %#v, %v", status, err)
	}
	login, err = client.LoginWebAuthWithResponse(t.Context(), managementv1.LocalLoginRequest{
		Username: browserAuthTestUsername, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, CSRFToken: &csrf},
	})
	if err != nil || login.JSON200 == nil || login.JSON200.MFA == nil || login.JSON200.Credentials != nil {
		t.Fatalf("MFA login continuation = %#v, %v", login, err)
	}
	invalidAssertion := "{}"
	failed, err := client.FinishWebAuthLoginWithResponse(t.Context(), managementv1.WebAuthLoginMFAFinishRequest{
		TransactionID: login.JSON200.MFA.TransactionID, BrowserBinding: &binding, CSRFToken: &csrf, CredentialResponse: &invalidAssertion,
	})
	if err != nil || failed.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("invalid MFA assertion = %#v, %v", failed, err)
	}
	disabled, err := client.DisableWebAuthMFAWithResponse(t.Context(), managementv1.WebAuthMFADisableRequest{
		Password: &password, Confirm: managementv1.WebAuthMFADisableRequestConfirm(true),
	}, credentialRequestEditor(verified.AccessToken, verified.CSRFToken))
	if err != nil || disabled.JSON200 == nil {
		t.Fatalf("disable MFA = %#v, %v", disabled, err)
	}
}

func TestGeneratedClientSessionRevokeAndConcurrentRefresh(t *testing.T) {
	handler, _ := newBrowserAuthTestHandler(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	first := generatedLogin(t, client)
	second := generatedLogin(t, client)
	secondPrincipal, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(second.AccessToken, ""))
	if err != nil || secondPrincipal.JSON200 == nil {
		t.Fatalf("second principal = %#v, %v", secondPrincipal, err)
	}
	revoked, err := client.RevokeWebAuthSessionWithResponse(t.Context(), secondPrincipal.JSON200.WebAuthSessionID,
		managementv1.WebAuthSessionRevokeRequest{ConfirmCurrent: false}, credentialRequestEditor(first.AccessToken, first.CSRFToken))
	if err != nil || revoked.StatusCode() != http.StatusNoContent {
		t.Fatalf("revoke second session = %#v, %v", revoked, err)
	}
	denied, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(second.AccessToken, ""))
	if err != nil || denied.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("revoked second principal = %#v, %v", denied, err)
	}
	firstPrincipal, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(first.AccessToken, ""))
	if err != nil || firstPrincipal.JSON200 == nil {
		t.Fatalf("first principal = %#v, %v", firstPrincipal, err)
	}
	unconfirmed, err := client.RevokeWebAuthSessionWithResponse(t.Context(), firstPrincipal.JSON200.WebAuthSessionID,
		managementv1.WebAuthSessionRevokeRequest{ConfirmCurrent: false}, credentialRequestEditor(first.AccessToken, first.CSRFToken))
	if err != nil || unconfirmed.StatusCode() != http.StatusConflict {
		t.Fatalf("unconfirmed current revoke = %#v, %v", unconfirmed, err)
	}

	refreshCredentials := generatedLogin(t, client)
	responses := make(chan *managementv1.RefreshWebAuthResponse, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, refreshErr := client.RefreshWebAuthWithResponse(t.Context(), credentialRequestEditor(refreshCredentials.RefreshToken, refreshCredentials.CSRFToken))
			responses <- response
			errors <- refreshErr
		}()
	}
	wait.Wait()
	close(responses)
	close(errors)
	for refreshErr := range errors {
		if refreshErr != nil {
			t.Fatal(refreshErr)
		}
	}
	statusCounts := map[int]int{}
	var rotated *managementv1.WebAuthCredentials
	for response := range responses {
		statusCounts[response.StatusCode()]++
		if response.JSON200 != nil {
			rotated = response.JSON200
		}
	}
	if statusCounts[http.StatusOK] != 1 || statusCounts[http.StatusConflict] != 1 || rotated == nil {
		t.Fatalf("concurrent refresh statuses = %v", statusCounts)
	}
	current, err := client.GetWebAuthPrincipalWithResponse(t.Context(), credentialRequestEditor(rotated.AccessToken, ""))
	if err != nil || current.JSON200 == nil {
		t.Fatalf("rotated principal after conflict = %#v, %v", current, err)
	}
}

const (
	browserAuthTestUsername = "admin"
	browserAuthTestPassword = "correct horse battery staple"
)

func newBrowserAuthTestHandler(t *testing.T) (http.Handler, *browserauth.Service) {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Now().UTC()
	hash, err := userpassword.Hash([]byte(browserAuthTestPassword))
	if err != nil {
		t.Fatal(err)
	}
	user := usercmd.User{
		ID: browserAuthTestUsername, Username: browserAuthTestUsername, NormalizedUsername: browserAuthTestUsername, DisplayName: "Administrator",
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Primary: true, Version: 1,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now,
	}
	audit := usercmd.AuditEvent{ID: "create-admin", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: now}
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
		t.Fatal(err)
	}
	service, err := browserauth.NewService(provider.Users(), browserauth.Config{AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, Origin: "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := (Config{Enabled: true, ListenAddr: "127.0.0.1:8081", OperatorToken: "operator-secret"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return config.Handler(Services{BrowserAuth: service}), service
}

func operatorRequestEditor(_ context.Context, request *http.Request) error {
	request.Header.Set(authorizationHeader, "Bearer operator-secret")
	return nil
}

func credentialRequestEditor(credential, csrf string) managementv1.RequestEditorFn {
	return func(_ context.Context, request *http.Request) error {
		if strings.Contains(request.URL.Path, "/refresh") {
			request.Header.Set(webAuthRefreshHeader, credential)
		} else {
			request.Header.Set(webAuthAccessHeader, credential)
		}
		if csrf != "" {
			request.Header.Set(csrfHeader, csrf)
		}
		return nil
	}
}

func generatedLogin(t *testing.T, client *managementv1.ClientWithResponses) managementv1.WebAuthCredentials {
	t.Helper()
	password := browserAuthTestPassword
	binding, csrf := strings.Repeat("b", 32), strings.Repeat("c", 32)
	response, err := client.LoginWebAuthWithResponse(t.Context(), managementv1.LocalLoginRequest{
		Username: browserAuthTestUsername, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, CSRFToken: &csrf},
	})
	if err != nil || response.JSON200 == nil || response.JSON200.Credentials == nil {
		t.Fatalf("login = %#v, %v", response, err)
	}
	return *response.JSON200.Credentials
}

func publicKeyChallenge(options map[string]interface{}) (string, bool) {
	publicKey, ok := options["publicKey"].(map[string]interface{})
	if !ok {
		return "", false
	}
	challenge, ok := publicKey["challenge"].(string)
	return challenge, ok && challenge != ""
}

func registrationResponse(t *testing.T, challenge string) string {
	t.Helper()
	const (
		attestationHex = "a363666d74646e6f6e656761747453746d74a068617574684461746158a4bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b559000000008446ccb9ab1db374750b2367ff6f3a1f0020f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4a5010203262001215820afefa16f97ca9b2d23eb86ccb64098d20db90856062eb249c33a9b672f26df61225820930a56b87a2fca66334b03458abf879717c12cc68ed73290af2e2664796b9220"
		clientHex      = "7b2274797065223a22776562617574686e2e637265617465222c226368616c6c656e6765223a22414d4d507434557878475453746e63647134313759447742466938767049612d7077386f4f755657345441222c226f726967696e223a2268747470733a2f2f6578616d706c652e6f7267222c2263726f73734f726967696e223a66616c73652c22657874726144617461223a22636c69656e74446174614a534f4e206d617920626520657874656e6465642077697468206164646974696f6e616c206669656c647320696e20746865206675747572652c207375636820617320746869733a20426b5165446a646354427258426941774a544c453551227d"
		credentialHex  = "f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4"
		rpHashHex      = "bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b5"
	)
	attestation := decodeHex(t, strings.Replace(attestationHex, rpHashHex+"59", rpHashHex+"5d", 1))
	clientData := decodeHex(t, clientHex)
	var client map[string]interface{}
	if err := json.Unmarshal(clientData, &client); err != nil {
		t.Fatal(err)
	}
	client["challenge"] = challenge
	client["origin"] = "https://example.org"
	clientData, err := json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := decodeHex(t, credentialHex)
	response := map[string]interface{}{
		"id": base64.RawURLEncoding.EncodeToString(credentialID), "rawId": base64.RawURLEncoding.EncodeToString(credentialID), "type": "public-key",
		"response": map[string]interface{}{
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
		},
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
