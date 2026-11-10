package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) MCPConnections(ctx context.Context, auth Auth) (managementv1.MCPInventory, error) {
	r, err := c.generated.ListMCPConnectionsWithResponse(ctx, authEditors(auth)...)
	if err != nil || r == nil {
		return managementv1.MCPInventory{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPInventory{}, responseError(r.StatusCode())
}
func (c *Client) CreateMCPConnection(ctx context.Context, auth Auth, request managementv1.MCPCreateRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.CreateMCPConnectionWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	return mcpItemResponse(r, e)
}
func (c *Client) ProbeMCPConnection(ctx context.Context, auth Auth, request managementv1.MCPCreateRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.ProbeMCPConnectionWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPItem{}, responseError(r.StatusCode())
}
func (c *Client) UpdateMCPConnection(ctx context.Context, auth Auth, id string, request managementv1.MCPUpdateRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.UpdateMCPConnectionWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	return mcpItemResponse(r, e)
}
func (c *Client) ProbeMCPConnectionUpdate(ctx context.Context, auth Auth, id string, request managementv1.MCPUpdateRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.ProbeMCPConnectionUpdateWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPItem{}, responseError(r.StatusCode())
}
func (c *Client) SelectMCPConnection(ctx context.Context, auth Auth, id string, request managementv1.MCPSelectionRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.SelectMCPConnectionWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	return mcpItemResponse(r, e)
}
func (c *Client) DeleteMCPConnection(ctx context.Context, auth Auth, id string, request managementv1.MCPSelectionRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.DeleteMCPConnectionWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	return mcpItemResponse(r, e)
}
func (c *Client) DiagnoseMCPConnection(ctx context.Context, auth Auth, id string) (managementv1.MCPDiagnostic, error) {
	r, e := c.generated.DiagnoseMCPConnectionWithResponse(ctx, id, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPDiagnostic{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPDiagnostic{}, responseError(r.StatusCode())
}

func mcpItemResponse[T interface{ StatusCode() int }](r T, err error) (managementv1.MCPItem, error) {
	if err != nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	switch v := any(r).(type) {
	case *managementv1.CreateMCPConnectionResponse:
		if v != nil && v.JSON200 != nil {
			return *v.JSON200, nil
		}
	case *managementv1.UpdateMCPConnectionResponse:
		if v != nil && v.JSON200 != nil {
			return *v.JSON200, nil
		}
	case *managementv1.SelectMCPConnectionResponse:
		if v != nil && v.JSON200 != nil {
			return *v.JSON200, nil
		}
	case *managementv1.DeleteMCPConnectionResponse:
		if v != nil && v.JSON200 != nil {
			return *v.JSON200, nil
		}
	}
	return managementv1.MCPItem{}, responseError(r.StatusCode())
}
