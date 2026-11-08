package managementclient

import (
	"context"
	"errors"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

var (
	ErrInvalid   = errors.New("management request is invalid")
	ErrForbidden = errors.New("web authentication proof was rejected")
	ErrConflict  = errors.New("web authentication state changed concurrently")
	ErrNotFound  = errors.New("management resource was not found")
)

// Auth carries opaque browser credentials for one Core operation.
type Auth struct {
	Access  string
	Refresh string
	CSRF    string
}

func (c *Client) Login(ctx context.Context, request managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error) {
	response, err := c.generated.LoginWebAuthWithResponse(ctx, request)
	if err != nil || response == nil {
		return managementv1.WebAuthLoginResult{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthLoginResult{}, responseError(response.StatusCode())
}

func (c *Client) FinishLogin(ctx context.Context, request managementv1.WebAuthLoginMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	response, err := c.generated.FinishWebAuthLoginWithResponse(ctx, request)
	if err != nil || response == nil {
		return managementv1.WebAuthCredentials{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
}

func (c *Client) Principal(ctx context.Context, auth Auth) (managementv1.WebAuthPrincipal, error) {
	response, err := c.generated.GetWebAuthPrincipalWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthPrincipal{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthPrincipal{}, responseError(response.StatusCode())
}

func (c *Client) Refresh(ctx context.Context, auth Auth) (managementv1.WebAuthCredentials, error) {
	response, err := c.generated.RefreshWebAuthWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthCredentials{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
}

func (c *Client) Logout(ctx context.Context, auth Auth) error {
	response, err := c.generated.LogoutWebAuthWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

func (c *Client) ReplacePassword(ctx context.Context, auth Auth, request managementv1.WebAuthPasswordReplaceRequest) (managementv1.WebAuthCredentials, error) {
	response, err := c.generated.ReplaceWebAuthPasswordWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthCredentials{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
}

func (c *Client) MFAStatus(ctx context.Context, auth Auth) (managementv1.WebAuthMFAStatus, error) {
	response, err := c.generated.GetWebAuthMFAStatusWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthMFAStatus{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthMFAStatus{}, responseError(response.StatusCode())
}

func (c *Client) BeginMFA(ctx context.Context, auth Auth, replace bool, password *string, binding *string) (managementv1.WebAuthMFAContinuation, error) {
	if replace {
		response, err := c.generated.BeginWebAuthMFAReplaceWithResponse(ctx, managementv1.WebAuthMFAReplaceBeginRequest{
			BrowserBinding: binding, Confirm: true, Password: password,
		}, authEditors(auth)...)
		if err != nil || response == nil {
			return managementv1.WebAuthMFAContinuation{}, ErrUnavailable
		}
		if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
			return *response.JSON200, nil
		}
		return managementv1.WebAuthMFAContinuation{}, responseError(response.StatusCode())
	}
	response, err := c.generated.BeginWebAuthMFAEnableWithResponse(ctx, managementv1.WebAuthMFABeginRequest{
		BrowserBinding: binding, Password: password,
	}, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthMFAContinuation{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthMFAContinuation{}, responseError(response.StatusCode())
}

func (c *Client) FinishMFA(ctx context.Context, auth Auth, replace bool, request managementv1.WebAuthMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	if replace {
		response, err := c.generated.FinishWebAuthMFAReplaceWithResponse(ctx, request, authEditors(auth)...)
		if err != nil || response == nil {
			return managementv1.WebAuthCredentials{}, ErrUnavailable
		}
		if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
			return *response.JSON200, nil
		}
		return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
	}
	response, err := c.generated.FinishWebAuthMFAEnableWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthCredentials{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
}

func (c *Client) DisableMFA(ctx context.Context, auth Auth, password *string) (managementv1.WebAuthCredentials, error) {
	response, err := c.generated.DisableWebAuthMFAWithResponse(ctx, managementv1.WebAuthMFADisableRequest{
		Confirm: true, Password: password,
	}, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthCredentials{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthCredentials{}, responseError(response.StatusCode())
}

func (c *Client) Sessions(ctx context.Context, auth Auth) (managementv1.WebAuthSessionPage, error) {
	response, err := c.generated.ListWebAuthSessionsWithResponse(ctx, nil, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.WebAuthSessionPage{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.WebAuthSessionPage{}, responseError(response.StatusCode())
}

func (c *Client) RevokeSession(ctx context.Context, auth Auth, sessionID string, confirmCurrent bool) error {
	response, err := c.generated.RevokeWebAuthSessionWithResponse(ctx, sessionID, managementv1.WebAuthSessionRevokeRequest{
		ConfirmCurrent: confirmCurrent,
	}, authEditors(auth)...)
	if err != nil || response == nil {
		return ErrUnavailable
	}
	if response.StatusCode() == http.StatusNoContent {
		return nil
	}
	return responseError(response.StatusCode())
}

func authEditors(auth Auth) []managementv1.RequestEditorFn {
	return []managementv1.RequestEditorFn{func(_ context.Context, request *http.Request) error {
		if auth.Access != "" {
			request.Header.Set("X-Balda-WebAuth-Access", auth.Access)
		}
		if auth.Refresh != "" {
			request.Header.Set("X-Balda-WebAuth-Refresh", auth.Refresh)
		}
		if auth.CSRF != "" {
			request.Header.Set("X-Balda-CSRF", auth.CSRF)
		}
		return nil
	}}
}

func responseError(status int) error {
	switch status {
	case http.StatusBadRequest:
		return ErrInvalid
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusConflict:
		return ErrConflict
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrUnavailable
	default:
		return ErrInternal
	}
}
