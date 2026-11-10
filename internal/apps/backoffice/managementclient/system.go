package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) SystemOverview(ctx context.Context, auth Auth) (managementv1.SystemOverview, error) {
	response, err := c.generated.GetSystemOverviewWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SystemOverview{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SystemOverview{}, responseError(response.StatusCode())
}

func (c *Client) SystemAgent(ctx context.Context, auth Auth) (managementv1.SystemAgent, error) {
	response, err := c.generated.GetSystemAgentWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SystemAgent{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SystemAgent{}, responseError(response.StatusCode())
}
