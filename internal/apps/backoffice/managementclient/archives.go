package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) AISessionArchives(ctx context.Context, auth Auth, params *managementv1.ListAISessionArchivesParams) (managementv1.AISessionArchiveInventory, error) {
	response, err := c.generated.ListAISessionArchivesWithResponse(ctx, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.AISessionArchiveInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.AISessionArchiveInventory{}, responseError(response.StatusCode())
}

func (c *Client) AISessionArchive(ctx context.Context, auth Auth, rawID string, params *managementv1.GetAISessionArchiveParams) (managementv1.AISessionArchiveTranscript, error) {
	if rawID == "" || len(rawID) > 64 {
		return managementv1.AISessionArchiveTranscript{}, ErrInvalid
	}
	response, err := c.generated.GetAISessionArchiveWithResponse(ctx, rawID, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.AISessionArchiveTranscript{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.AISessionArchiveTranscript{}, responseError(response.StatusCode())
}
