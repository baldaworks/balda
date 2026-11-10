package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

// Hooks returns the complete managed-hook configuration without executing it.
func (c *Client) Hooks(ctx context.Context, auth Auth) (managementv1.HookInventory, error) {
	response, err := c.generated.ListHooksWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.HookInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.HookInventory{}, responseError(response.StatusCode())
}

// CreateHook creates one managed hook against the observed record version.
func (c *Client) CreateHook(ctx context.Context, auth Auth, request managementv1.HookSaveRequest) error {
	response, err := c.generated.CreateHookWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

// UpdateHook updates one managed hook against the observed record version.
func (c *Client) UpdateHook(ctx context.Context, auth Auth, name string, request managementv1.HookSaveRequest) error {
	response, err := c.generated.UpdateHookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

// DeleteHook deletes one managed hook against the observed record version.
func (c *Client) DeleteHook(ctx context.Context, auth Auth, name string, request managementv1.HookDeleteRequest) error {
	response, err := c.generated.DeleteHookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
