package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) Users(ctx context.Context, auth Auth) (managementv1.UserInventory, error) {
	response, err := c.generated.ListUsersWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.UserInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.UserInventory{}, responseError(response.StatusCode())
}

func (c *Client) User(ctx context.Context, auth Auth, userID string) (managementv1.UserRecord, error) {
	response, err := c.generated.GetUserWithResponse(ctx, userID, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.UserRecord{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.UserRecord{}, responseError(response.StatusCode())
}

func (c *Client) UserSessions(ctx context.Context, auth Auth, userID string) (managementv1.WebAuthSessionPage, error) {
	response, err := c.generated.ListWebAuthSessionsWithResponse(ctx, &managementv1.ListWebAuthSessionsParams{UserID: &userID}, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthSessionPage{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthSessionPage{}, responseError(response.StatusCode())
}

func (c *Client) CreateUser(ctx context.Context, auth Auth, request managementv1.UserCreateRequest) (managementv1.UserRecord, error) {
	response, err := c.generated.CreateUserWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.UserRecord{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.UserRecord{}, responseError(response.StatusCode())
}

func (c *Client) UpdateUser(ctx context.Context, auth Auth, userID string, request managementv1.UserUpdateRequest) (managementv1.UserRecord, error) {
	response, err := c.generated.UpdateUserWithResponse(ctx, userID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.UserRecord{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.UserRecord{}, responseError(response.StatusCode())
}

func (c *Client) ResetUserCredential(ctx context.Context, auth Auth, userID string, request managementv1.UserCredentialResetRequest) error {
	response, err := c.generated.ResetUserCredentialWithResponse(ctx, userID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
