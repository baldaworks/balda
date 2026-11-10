package managementclient

import (
	"context"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"net/http"
)

func (c *Client) Aliases(ctx context.Context, auth Auth) (managementv1.AliasInventory, error) {
	r, e := c.generated.ListAliasesWithResponse(ctx, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.AliasInventory{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.AliasInventory{}, responseError(r.StatusCode())
}
func (c *Client) Alias(ctx context.Context, auth Auth, name string) (managementv1.AliasRecord, error) {
	r, e := c.generated.GetAliasWithResponse(ctx, name, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.AliasRecord{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.AliasRecord{}, responseError(r.StatusCode())
}
func (c *Client) CreateAlias(ctx context.Context, auth Auth, request managementv1.AliasCreateRequest) (managementv1.AliasRecord, error) {
	r, e := c.generated.CreateAliasWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.AliasRecord{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.AliasRecord{}, responseError(r.StatusCode())
}
func (c *Client) RetargetAlias(ctx context.Context, auth Auth, name string, request managementv1.AliasRetargetRequest) (managementv1.AliasRecord, error) {
	r, e := c.generated.RetargetAliasWithResponse(ctx, name, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.AliasRecord{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.AliasRecord{}, responseError(r.StatusCode())
}
func (c *Client) DeleteAlias(ctx context.Context, auth Auth, name string, request managementv1.AliasDeleteRequest) error {
	r, e := c.generated.DeleteAliasWithResponse(ctx, name, request, authEditors(auth)...)
	if e != nil || r == nil {
		return ErrUnavailable
	}
	if r.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(r.StatusCode())
}
