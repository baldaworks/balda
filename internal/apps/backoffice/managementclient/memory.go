package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) MemoryFacts(ctx context.Context, auth Auth, query string) (managementv1.MemoryFactInventory, error) {
	params := &managementv1.ListMemoryFactsParams{}
	if query != "" {
		params.Q = &query
	}
	response, err := c.generated.ListMemoryFactsWithResponse(ctx, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.MemoryFactInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.MemoryFactInventory{}, responseError(response.StatusCode())
}

func (c *Client) MemoryFact(ctx context.Context, auth Auth, id int64) (managementv1.MemoryFactEditor, error) {
	response, err := c.generated.GetMemoryFactWithResponse(ctx, id, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.MemoryFactEditor{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.MemoryFactEditor{}, responseError(response.StatusCode())
}

func (c *Client) CreateMemoryFact(ctx context.Context, auth Auth, request managementv1.MemoryFactCreateRequest) (managementv1.MemoryFact, error) {
	response, err := c.generated.CreateMemoryFactWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.MemoryFact{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.MemoryFact{}, responseError(response.StatusCode())
}

func (c *Client) UpdateMemoryFact(ctx context.Context, auth Auth, id int64, request managementv1.MemoryFactUpdateRequest) (managementv1.MemoryFact, error) {
	response, err := c.generated.UpdateMemoryFactWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.MemoryFact{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.MemoryFact{}, responseError(response.StatusCode())
}

func (c *Client) DeleteMemoryFact(ctx context.Context, auth Auth, id int64, request managementv1.MemoryFactDeleteRequest) error {
	response, err := c.generated.DeleteMemoryFactWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}
