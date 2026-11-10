package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) Webhooks(ctx context.Context, auth Auth) (managementv1.WebhookInventory, error) {
	response, err := c.generated.ListWebhooksWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookInventory{}, responseError(response.StatusCode())
}

func (c *Client) Webhook(ctx context.Context, auth Auth, name string) (managementv1.WebhookItem, error) {
	response, err := c.generated.GetWebhookWithResponse(ctx, name, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookItem{}, responseError(response.StatusCode())
}

func (c *Client) CreateWebhook(ctx context.Context, auth Auth, request managementv1.WebhookCreateRequest) (managementv1.WebhookSecretResult, error) {
	response, err := c.generated.CreateWebhookWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookSecretResult{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookSecretResult{}, responseError(response.StatusCode())
}

func (c *Client) UpdateWebhook(ctx context.Context, auth Auth, name string, request managementv1.WebhookUpdateRequest) (managementv1.WebhookItem, error) {
	response, err := c.generated.UpdateWebhookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookItem{}, responseError(response.StatusCode())
}

func (c *Client) SelectWebhook(ctx context.Context, auth Auth, name string, request managementv1.WebhookSelectionRequest) (managementv1.WebhookItem, error) {
	response, err := c.generated.SelectWebhookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookItem{}, responseError(response.StatusCode())
}

func (c *Client) DeleteWebhook(ctx context.Context, auth Auth, name string, request managementv1.WebhookVersionRequest) (managementv1.WebhookItem, error) {
	response, err := c.generated.DeleteWebhookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookItem{}, responseError(response.StatusCode())
}

func (c *Client) RotateWebhook(ctx context.Context, auth Auth, name string, request managementv1.WebhookVersionRequest) (managementv1.WebhookSecretResult, error) {
	response, err := c.generated.RotateWebhookSecretWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookSecretResult{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookSecretResult{}, responseError(response.StatusCode())
}

func (c *Client) TestWebhook(ctx context.Context, auth Auth, name string, request managementv1.WebhookTestRequest) (managementv1.WebhookTestResult, error) {
	response, err := c.generated.TestWebhookWithResponse(ctx, name, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookTestResult{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookTestResult{}, responseError(response.StatusCode())
}

func (c *Client) WebhookHistory(ctx context.Context, auth Auth, name string, params *managementv1.ListWebhookHistoryParams) (managementv1.WebhookHistoryPage, error) {
	response, err := c.generated.ListWebhookHistoryWithResponse(ctx, name, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookHistoryPage{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookHistoryPage{}, responseError(response.StatusCode())
}

func (c *Client) WebhookHistoryDetail(ctx context.Context, auth Auth, name, jobID string) (managementv1.WebhookHistoryDetail, error) {
	response, err := c.generated.GetWebhookHistoryWithResponse(ctx, name, jobID, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebhookHistoryDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebhookHistoryDetail{}, responseError(response.StatusCode())
}
