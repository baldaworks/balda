package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientBindingInvitationLifecycle(t *testing.T) {
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedProviderLogin(t, client, "admin")
	auth := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	channels, err := client.ListBindingChannelsWithResponse(t.Context(), auth)
	if err != nil || channels.JSON200 == nil || len(channels.JSON200.Channels) != 1 || !channels.JSON200.Channels[0].Ready {
		t.Fatalf("channels = %#v, %v", channels, err)
	}
	issued, err := client.IssueBindingInvitationWithResponse(t.Context(), "operator", managementv1.BindingInvitationIssueRequest{
		ChannelType: "telegram", ExpectedUserVersion: 1, Replace: false,
	}, auth)
	if err != nil || issued.JSON200 == nil || issued.JSON200.Proof == "" || issued.HTTPResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("issued = %#v, %v", issued, err)
	}
	proof := issued.JSON200.Proof
	inventory, err := client.ListUserBindingsWithResponse(t.Context(), "operator", auth)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Invitations) != 1 || strings.Contains(string(inventory.Body), proof) || strings.Contains(string(inventory.Body), "token") {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	invitation := inventory.JSON200.Invitations[0]
	cancelled, err := client.CancelBindingInvitationWithResponse(t.Context(), "operator", invitation.InvitationID, managementv1.BindingInvitationCancelRequest{
		ExpectedInvitationVersion: invitation.Version, Confirm: managementv1.BindingInvitationCancelRequestConfirm(true),
	}, auth)
	if err != nil || cancelled.StatusCode() != http.StatusNoContent {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
}

func TestBindingChannelInventoryDoesNotRefreshProvider(t *testing.T) {
	_, browserAuth := newBrowserAuthTestHandler(t)
	credentials, err := browserAuth.Login(t.Context(), browserAuthTestUsername, []byte(browserAuthTestPassword))
	if err != nil {
		t.Fatal(err)
	}
	channels := &countingBindingChannels{items: []usercmd.BindingChannel{{Integration: usercmd.BindingIntegration{ChannelType: "telegram"}}}}
	config, err := (Config{Enabled: true, ListenAddr: "127.0.0.1:8081", OperatorToken: "operator-secret"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	handler := config.Handler(Services{BrowserAuth: browserAuth, Invitations: noOpInvitations{}, Channels: channels})
	request := httptest.NewRequest(http.MethodGet, "/api/management/v1/binding-channels", nil)
	request.Header.Set(authorizationHeader, "Bearer operator-secret")
	request.Header.Set(webAuthAccessHeader, credentials.AccessToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || channels.listCalls != 1 || channels.refreshCalls != 0 {
		t.Fatalf("status=%d list=%d refresh=%d body=%s", response.Code, channels.listCalls, channels.refreshCalls, response.Body.String())
	}
}

type countingBindingChannels struct {
	items                   []usercmd.BindingChannel
	listCalls, refreshCalls int
}

func (c *countingBindingChannels) Get(string) (usercmd.BindingChannel, bool) {
	return usercmd.BindingChannel{}, false
}
func (c *countingBindingChannels) List() []usercmd.BindingChannel {
	c.listCalls++
	return c.items
}
func (c *countingBindingChannels) Refresh(context.Context, string) (usercmd.BindingChannel, error) {
	c.refreshCalls++
	return usercmd.BindingChannel{}, usercmd.ErrBindingInvitationUnavailable
}

type noOpInvitations struct{}

func (noOpInvitations) Issue(context.Context, usercmd.InvitationActor, string, uint64, usercmd.BindingIntegration, bool) (usercmd.IssuedBindingInvitation, error) {
	return usercmd.IssuedBindingInvitation{}, usercmd.ErrBindingInvitationUnavailable
}
func (noOpInvitations) Pending(context.Context, string) ([]usercmd.BindingInvitation, error) {
	return nil, nil
}
func (noOpInvitations) Cancel(context.Context, usercmd.InvitationActor, string, string, uint64) error {
	return nil
}
