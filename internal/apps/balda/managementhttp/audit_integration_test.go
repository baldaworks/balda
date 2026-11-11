package managementhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientCanonicalAuditProjection(t *testing.T) {
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	admin := generatedProviderLogin(t, client, "admin")
	action := "web_auth.login.succeeded"
	page, err := client.ListAuditEventsWithResponse(t.Context(), &managementv1.ListAuditEventsParams{Action: &action}, credentialRequestEditor(admin.AccessToken, ""))
	if err != nil || page.JSON200 == nil || len(page.JSON200.Events) != 1 {
		t.Fatalf("audit page = %#v, %v", page, err)
	}
	event := page.JSON200.Events[0]
	if event.Action != action || event.ActorWebAuthSessionID == nil || event.TargetType != "web_auth_session" {
		t.Fatalf("audit event = %#v", event)
	}
	if event.ActorUserID == nil || *event.ActorUserID != redactedAuditID {
		t.Fatalf("non-canonical actor ID was not redacted: %#v", event.ActorUserID)
	}
	body := string(page.Body)
	for _, forbidden := range []string{"encoding", "legacy_web_auth", "source", "reason", admin.AccessToken, admin.RefreshToken} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("audit response disclosed %q: %s", forbidden, body)
		}
	}

	operator := generatedProviderLogin(t, client, "operator")
	denied, err := client.ListAuditEventsWithResponse(t.Context(), nil, credentialRequestEditor(operator.AccessToken, ""))
	if err != nil || denied.StatusCode() != http.StatusForbidden {
		t.Fatalf("operator audit = %#v, %v", denied, err)
	}
}
