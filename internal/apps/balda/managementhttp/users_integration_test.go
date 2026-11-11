package managementhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientCanonicalUserLifecycle(t *testing.T) {
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	admin := generatedProviderLogin(t, client, "admin")
	auth := credentialRequestEditor(admin.AccessToken, admin.CSRFToken)
	password := "temporary battery staple"
	created, err := client.CreateUserWithResponse(t.Context(), managementv1.UserCreateRequest{
		DisplayName: "Second Operator", Username: "second", TemporaryPassword: &password,
		Role: managementv1.Operator, Status: managementv1.UserStatusActive,
	}, auth)
	if err != nil || created.JSON200 == nil {
		t.Fatalf("create = %#v, %v", created, err)
	}
	if created.JSON200.CredentialState != managementv1.UserCredentialStateTemporary || !created.JSON200.CredentialMustChange || strings.Contains(string(created.Body), password) {
		t.Fatalf("unsafe create response = %s", created.Body)
	}
	userID := created.JSON200.UserID
	inventory, err := client.ListUsersWithResponse(t.Context(), auth)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Users) != 3 {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	updated, err := client.UpdateUserWithResponse(t.Context(), userID, managementv1.UserUpdateRequest{
		DisplayName: "Renamed Operator", Username: "second", Role: managementv1.Operator,
		Status: managementv1.UserStatusActive, ExpectedVersion: created.JSON200.Version, AcknowledgeBotAccessImpact: false,
	}, auth)
	if err != nil || updated.JSON200 == nil || updated.JSON200.DisplayName != "Renamed Operator" {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	replacement := "replacement battery staple"
	reset, err := client.ResetUserCredentialWithResponse(t.Context(), userID, managementv1.UserCredentialResetRequest{
		TemporaryPassword: &replacement, ExpectedUserVersion: updated.JSON200.Version,
		ExpectedCredentialVersion: updated.JSON200.CredentialVersion, ConfirmCurrent: false,
	}, auth)
	if err != nil || reset.StatusCode() != http.StatusNoContent || strings.Contains(string(reset.Body), replacement) {
		t.Fatalf("reset = %#v, %v", reset, err)
	}
	read, err := client.GetUserWithResponse(t.Context(), userID, auth)
	if err != nil || read.JSON200 == nil || read.JSON200.CredentialVersion != updated.JSON200.CredentialVersion+1 {
		t.Fatalf("read = %#v, %v", read, err)
	}
}

func TestCanonicalUserRoutesRequireAdminAndCSRF(t *testing.T) {
	handler := newManagementTestHandler(t, nil, nil, nil, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	operator := generatedProviderLogin(t, client, "operator")
	denied, err := client.ListUsersWithResponse(t.Context(), credentialRequestEditor(operator.AccessToken, ""))
	if err != nil || denied.StatusCode() != http.StatusForbidden {
		t.Fatalf("operator list = %#v, %v", denied, err)
	}
	admin := generatedProviderLogin(t, client, "admin")
	password := "temporary battery staple"
	missingCSRF, err := client.CreateUserWithResponse(t.Context(), managementv1.UserCreateRequest{
		DisplayName: "Denied", Username: "denied", TemporaryPassword: &password,
		Role: managementv1.Operator, Status: managementv1.UserStatusActive,
	}, credentialRequestEditor(admin.AccessToken, ""))
	if err != nil || missingCSRF.StatusCode() != http.StatusForbidden || strings.Contains(string(missingCSRF.Body), password) {
		t.Fatalf("missing csrf = %#v, %v", missingCSRF, err)
	}
}
