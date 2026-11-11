package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) BindingChannels(ctx context.Context, auth Auth) (managementv1.BindingChannelInventory, error) {
	response, err := c.generated.ListBindingChannelsWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.BindingChannelInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.BindingChannelInventory{}, responseError(response.StatusCode())
}

func (c *Client) RefreshBindingChannel(ctx context.Context, auth Auth, channel string) (managementv1.BindingChannel, error) {
	response, err := c.generated.RefreshBindingChannelWithResponse(ctx, channel, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.BindingChannel{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.BindingChannel{}, responseError(response.StatusCode())
}

func (c *Client) UserBindings(ctx context.Context, auth Auth, userID string) (managementv1.UserBindingInventory, error) {
	response, err := c.generated.ListUserBindingsWithResponse(ctx, userID, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.UserBindingInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.UserBindingInventory{}, responseError(response.StatusCode())
}

func (c *Client) DeleteUserBinding(ctx context.Context, auth Auth, userID, bindingID string, request managementv1.BindingDeleteRequest) error {
	response, err := c.generated.DeleteUserBindingWithResponse(ctx, userID, bindingID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

func (c *Client) IssueBindingInvitation(ctx context.Context, auth Auth, userID string, request managementv1.BindingInvitationIssueRequest) (managementv1.IssuedBindingInvitation, error) {
	response, err := c.generated.IssueBindingInvitationWithResponse(ctx, userID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.IssuedBindingInvitation{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.IssuedBindingInvitation{}, responseError(response.StatusCode())
}

func (c *Client) CancelBindingInvitation(ctx context.Context, auth Auth, userID, invitationID string, request managementv1.BindingInvitationCancelRequest) error {
	response, err := c.generated.CancelBindingInvitationWithResponse(ctx, userID, invitationID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
