package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) AuditEvents(ctx context.Context, auth Auth, params *managementv1.ListAuditEventsParams) (managementv1.AuditEventPage, error) {
	response, err := c.generated.ListAuditEventsWithResponse(ctx, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.AuditEventPage{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.AuditEventPage{}, responseError(response.StatusCode())
}
