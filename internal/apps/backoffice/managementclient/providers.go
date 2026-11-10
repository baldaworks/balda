package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

// Providers reads the current secret-free provider inventory.
func (c *Client) Providers(ctx context.Context, auth Auth) (managementv1.ProviderInventory, error) {
	response, err := c.generated.ListProvidersWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ProviderInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ProviderInventory{}, responseError(response.StatusCode())
}

// CreateProvider creates one managed provider without retrying ambiguous calls.
func (c *Client) CreateProvider(ctx context.Context, auth Auth, request managementv1.ProviderCreateRequest) error {
	response, err := c.generated.CreateProviderWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

// UpdateProvider replaces one managed provider definition.
func (c *Client) UpdateProvider(ctx context.Context, auth Auth, providerID string, request managementv1.ProviderUpdateRequest) error {
	response, err := c.generated.UpdateProviderWithResponse(ctx, providerID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

// SelectProvider changes the enabled state of one managed provider.
func (c *Client) SelectProvider(ctx context.Context, auth Auth, providerID string, request managementv1.ProviderSelectionRequest) error {
	response, err := c.generated.SelectProviderWithResponse(ctx, providerID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

// DeleteProvider tombstones one managed provider.
func (c *Client) DeleteProvider(ctx context.Context, auth Auth, providerID string, request managementv1.ProviderDeleteRequest) error {
	response, err := c.generated.DeleteProviderWithResponse(ctx, providerID, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
