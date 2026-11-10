package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) Plugins(ctx context.Context, auth Auth) (managementv1.PluginInventory, error) {
	response, err := c.generated.ListPluginsWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginInventory{}, responseError(response.StatusCode())
}

func (c *Client) Plugin(ctx context.Context, auth Auth, name string) (managementv1.PluginDetail, error) {
	response, err := c.generated.GetPluginWithResponse(ctx, name, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginDetail{}, responseError(response.StatusCode())
}

func (c *Client) ReviewPlugin(ctx context.Context, auth Auth, name, marketplace string) (managementv1.PluginCandidate, error) {
	params := &managementv1.ReviewPluginCandidateParams{}
	if marketplace != "" {
		params.Marketplace = &marketplace
	}
	response, err := c.generated.ReviewPluginCandidateWithResponse(ctx, name, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginCandidate{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginCandidate{}, responseError(response.StatusCode())
}

func (c *Client) MutatePlugin(ctx context.Context, auth Auth, name string, request managementv1.PluginMutationRequest) (managementv1.PluginMutationOutcome, error) {
	response, err := c.generated.MutatePluginWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginMutationOutcome{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginMutationOutcome{}, responseError(response.StatusCode())
}

func (c *Client) PluginMarketplaces(ctx context.Context, auth Auth) (managementv1.PluginMarketplaceInventory, error) {
	response, err := c.generated.ListPluginMarketplacesWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginMarketplaceInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginMarketplaceInventory{}, responseError(response.StatusCode())
}

func (c *Client) PluginMarketplace(ctx context.Context, auth Auth, name string) (managementv1.PluginMarketplaceDetail, error) {
	response, err := c.generated.GetPluginMarketplaceWithResponse(ctx, name, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginMarketplaceDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginMarketplaceDetail{}, responseError(response.StatusCode())
}

func (c *Client) AddPluginMarketplace(ctx context.Context, auth Auth, request managementv1.PluginMarketplaceAddRequest) (managementv1.PluginMarketplaceDetail, error) {
	response, err := c.generated.AddPluginMarketplaceWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginMarketplaceDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginMarketplaceDetail{}, responseError(response.StatusCode())
}

func (c *Client) RefreshPluginMarketplace(ctx context.Context, auth Auth, name string, request managementv1.PluginMarketplaceMutationRequest) (managementv1.PluginMarketplaceDetail, error) {
	response, err := c.generated.RefreshPluginMarketplaceWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.PluginMarketplaceDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.PluginMarketplaceDetail{}, responseError(response.StatusCode())
}

func (c *Client) RemovePluginMarketplace(ctx context.Context, auth Auth, name string, request managementv1.PluginMarketplaceMutationRequest) error {
	response, err := c.generated.RemovePluginMarketplaceWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
