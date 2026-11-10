package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) OnboardMCPBrowser(ctx context.Context, auth Auth, request managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPBrowserOnboardResult, error) {
	r, e := c.generated.OnboardMCPBrowserAuthorizationWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPBrowserOnboardResult{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPBrowserOnboardResult{}, responseError(r.StatusCode())
}
func (c *Client) OnboardMCPDevice(ctx context.Context, auth Auth, request managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPDeviceOnboardResult, error) {
	r, e := c.generated.OnboardMCPDeviceAuthorizationWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPDeviceOnboardResult{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPDeviceOnboardResult{}, responseError(r.StatusCode())
}
func (c *Client) BeginMCPBrowser(ctx context.Context, auth Auth, id string, request managementv1.MCPAuthorizationInput) (managementv1.MCPBrowserAuthorizationStart, error) {
	r, e := c.generated.BeginMCPBrowserAuthorizationWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPBrowserAuthorizationStart{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPBrowserAuthorizationStart{}, responseError(r.StatusCode())
}
func (c *Client) BeginMCPDevice(ctx context.Context, auth Auth, id string, request managementv1.MCPAuthorizationInput) (managementv1.MCPDeviceAuthorizationStart, error) {
	r, e := c.generated.BeginMCPDeviceAuthorizationWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPDeviceAuthorizationStart{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPDeviceAuthorizationStart{}, responseError(r.StatusCode())
}
func (c *Client) CompleteMCPBrowser(ctx context.Context, auth Auth, request managementv1.MCPBrowserCallback) (managementv1.MCPItem, error) {
	r, e := c.generated.CompleteMCPBrowserAuthorizationWithResponse(ctx, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPItem{}, responseError(r.StatusCode())
}
func (c *Client) MCPDeviceStatus(ctx context.Context, auth Auth, id string) (managementv1.MCPDeviceAuthorizationStatus, error) {
	r, e := c.generated.GetMCPDeviceAuthorizationWithResponse(ctx, id, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPDeviceAuthorizationStatus{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPDeviceAuthorizationStatus{}, responseError(r.StatusCode())
}
func (c *Client) CancelMCPAuthorization(ctx context.Context, auth Auth, id string) error {
	r, e := c.generated.CancelMCPAuthorizationWithResponse(ctx, id, authEditors(auth)...)
	if e != nil || r == nil {
		return ErrUnavailable
	}
	if r.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(r.StatusCode())
}
func (c *Client) DisconnectMCPAuthorization(ctx context.Context, auth Auth, id string) error {
	r, e := c.generated.DisconnectMCPAuthorizationWithResponse(ctx, id, authEditors(auth)...)
	if e != nil || r == nil {
		return ErrUnavailable
	}
	if r.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(r.StatusCode())
}
func (c *Client) RetryMCPAuthorization(ctx context.Context, auth Auth, id string, request managementv1.MCPRetryAuthorizationRequest) (managementv1.MCPItem, error) {
	r, e := c.generated.RetryMCPAuthorizationWithResponse(ctx, id, request, authEditors(auth)...)
	if e != nil || r == nil {
		return managementv1.MCPItem{}, ErrUnavailable
	}
	if r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		return *r.JSON200, nil
	}
	return managementv1.MCPItem{}, responseError(r.StatusCode())
}
