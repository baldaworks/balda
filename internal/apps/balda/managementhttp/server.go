package managementhttp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

type strictServer struct {
	browserAuth *browserauth.Service
	providers   *providers.Service
	mcp         mcpOperations
}

func (strictServer) GetManagementCompatibility(context.Context, managementv1.GetManagementCompatibilityRequestObject) (managementv1.GetManagementCompatibilityResponseObject, error) {
	return managementv1.GetManagementCompatibility200JSONResponse{
		ContractID:      managementv1.BaldaManagementV1,
		ContractVersion: managementv1.ContractVersion,
		APIMajor:        managementv1.N1,
	}, nil
}

func (s strictServer) LoginWebAuth(ctx context.Context, request managementv1.LoginWebAuthRequestObject) (managementv1.LoginWebAuthResponseObject, error) {
	if request.Body == nil || !boundedString(request.Body.Username, 1, 256) {
		return nil, usercmd.ErrInvalid
	}
	password, ok := requiredPointer(request.Body.Password, 1, 4096)
	browser, browserOK := requiredPointer(request.Body.BrowserContext.BrowserBinding, 16, 256)
	csrf, csrfOK := requiredPointer(request.Body.BrowserContext.CSRFToken, 16, 4096)
	if !ok || !browserOK || !csrfOK || !optionalPointer(request.Body.BrowserContext.DeviceLabel, 256) || !optionalPointer(request.Body.BrowserContext.ConnectionPeer, 256) {
		return nil, usercmd.ErrInvalid
	}
	passwordBytes := []byte(password)
	defer zero(passwordBytes)
	ctx = browserauth.WithSessionClient(ctx, pointerValue(request.Body.BrowserContext.DeviceLabel), pointerValue(request.Body.BrowserContext.ConnectionPeer))
	ctx = browserauth.WithMFABrowser(ctx, browser, csrf)
	credentials, err := s.browserAuth.Login(ctx, request.Body.Username, passwordBytes)
	if err != nil {
		return nil, err
	}
	result := managementv1.WebAuthLoginResult{Outcome: managementv1.Authenticated}
	if credentials.Pending != nil {
		continuation, mapErr := continuationDTO(*credentials.Pending)
		if mapErr != nil {
			return nil, mapErr
		}
		result.Outcome = managementv1.MFARequired
		result.MFA = &continuation
	} else {
		mapped := credentialsDTO(credentials)
		result.Credentials = &mapped
	}
	return managementv1.LoginWebAuth200JSONResponse(result), nil
}

func (s strictServer) FinishWebAuthLogin(ctx context.Context, request managementv1.FinishWebAuthLoginRequestObject) (managementv1.FinishWebAuthLoginResponseObject, error) {
	if request.Body == nil || !boundedString(request.Body.TransactionID, 1, 256) {
		return nil, usercmd.ErrInvalid
	}
	browser, browserOK := requiredPointer(request.Body.BrowserBinding, 16, 256)
	csrf, csrfOK := requiredPointer(request.Body.CSRFToken, 16, 4096)
	response, responseOK := requiredPointer(request.Body.CredentialResponse, 2, 131072)
	if !browserOK || !csrfOK || !responseOK || !optionalPointer(request.Body.DeviceLabel, 256) || !optionalPointer(request.Body.ConnectionPeer, 256) {
		return nil, usercmd.ErrInvalid
	}
	ctx = browserauth.WithSessionClient(ctx, pointerValue(request.Body.DeviceLabel), pointerValue(request.Body.ConnectionPeer))
	credentials, err := s.browserAuth.FinishLogin(ctx, request.Body.TransactionID, browser, csrf, []byte(response))
	if err != nil {
		return nil, err
	}
	return managementv1.FinishWebAuthLogin200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) LogoutWebAuth(ctx context.Context, _ managementv1.LogoutWebAuthRequestObject) (managementv1.LogoutWebAuthResponseObject, error) {
	if err := s.browserAuth.Logout(ctx, webAuthFromContext(ctx).access); err != nil {
		return nil, err
	}
	return managementv1.LogoutWebAuth204Response{}, nil
}

func (strictServer) GetWebAuthPrincipal(ctx context.Context, _ managementv1.GetWebAuthPrincipalRequestObject) (managementv1.GetWebAuthPrincipalResponseObject, error) {
	return managementv1.GetWebAuthPrincipal200JSONResponse(principalDTO(webAuthFromContext(ctx).principal)), nil
}

func (s strictServer) RefreshWebAuth(ctx context.Context, _ managementv1.RefreshWebAuthRequestObject) (managementv1.RefreshWebAuthResponseObject, error) {
	authority := webAuthFromContext(ctx)
	credentials, err := s.browserAuth.Refresh(ctx, authority.refresh, authority.csrf)
	if err != nil {
		return nil, err
	}
	return managementv1.RefreshWebAuth200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) ReplaceWebAuthPassword(ctx context.Context, request managementv1.ReplaceWebAuthPasswordRequestObject) (managementv1.ReplaceWebAuthPasswordResponseObject, error) {
	if request.Body == nil {
		return nil, usercmd.ErrInvalid
	}
	current, currentOK := requiredPointer(request.Body.CurrentPassword, 1, 4096)
	next, nextOK := requiredPointer(request.Body.NewPassword, 1, 4096)
	if !currentOK || !nextOK {
		return nil, usercmd.ErrInvalid
	}
	currentBytes, nextBytes := []byte(current), []byte(next)
	defer zero(currentBytes)
	defer zero(nextBytes)
	credentials, err := s.browserAuth.ReplacePassword(ctx, webAuthFromContext(ctx).access, currentBytes, nextBytes)
	if err != nil {
		return nil, err
	}
	return managementv1.ReplaceWebAuthPassword200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) GetWebAuthMFAStatus(ctx context.Context, _ managementv1.GetWebAuthMFAStatusRequestObject) (managementv1.GetWebAuthMFAStatusResponseObject, error) {
	status, err := s.browserAuth.MFAStatus(ctx, webAuthFromContext(ctx).access)
	if err != nil {
		return nil, err
	}
	result := managementv1.WebAuthMFAStatus{Enabled: status.Enabled, Available: status.Available}
	if !status.CreatedAt.IsZero() {
		result.CreatedAt = &status.CreatedAt
	}
	if !status.LastUsedAt.IsZero() {
		result.LastUsedAt = &status.LastUsedAt
	}
	return managementv1.GetWebAuthMFAStatus200JSONResponse(result), nil
}

func (s strictServer) BeginWebAuthMFAEnable(ctx context.Context, request managementv1.BeginWebAuthMFAEnableRequestObject) (managementv1.BeginWebAuthMFAEnableResponseObject, error) {
	if request.Body == nil {
		return nil, usercmd.ErrInvalid
	}
	password, passwordOK := requiredPointer(request.Body.Password, 1, 4096)
	browser, browserOK := requiredPointer(request.Body.BrowserBinding, 16, 256)
	if !passwordOK || !browserOK {
		return nil, usercmd.ErrInvalid
	}
	passwordBytes := []byte(password)
	defer zero(passwordBytes)
	authority := webAuthFromContext(ctx)
	start, err := s.browserAuth.BeginEnable(ctx, authority.access, passwordBytes, browser, authority.csrf)
	if err != nil {
		return nil, err
	}
	result, err := continuationDTO(start)
	if err != nil {
		return nil, err
	}
	return managementv1.BeginWebAuthMFAEnable200JSONResponse(result), nil
}

func (s strictServer) FinishWebAuthMFAEnable(ctx context.Context, request managementv1.FinishWebAuthMFAEnableRequestObject) (managementv1.FinishWebAuthMFAEnableResponseObject, error) {
	credentials, err := s.finishAccountMFA(ctx, request.Body, s.browserAuth.FinishEnable)
	if err != nil {
		return nil, err
	}
	return managementv1.FinishWebAuthMFAEnable200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) BeginWebAuthMFAReplace(ctx context.Context, request managementv1.BeginWebAuthMFAReplaceRequestObject) (managementv1.BeginWebAuthMFAReplaceResponseObject, error) {
	if request.Body == nil {
		return nil, usercmd.ErrInvalid
	}
	password, passwordOK := requiredPointer(request.Body.Password, 1, 4096)
	browser, browserOK := requiredPointer(request.Body.BrowserBinding, 16, 256)
	if !passwordOK || !browserOK || !bool(request.Body.Confirm) {
		return nil, usercmd.ErrInvalid
	}
	passwordBytes := []byte(password)
	defer zero(passwordBytes)
	authority := webAuthFromContext(ctx)
	start, err := s.browserAuth.BeginReplace(ctx, authority.access, passwordBytes, true, browser, authority.csrf)
	if err != nil {
		return nil, err
	}
	result, err := continuationDTO(start)
	if err != nil {
		return nil, err
	}
	return managementv1.BeginWebAuthMFAReplace200JSONResponse(result), nil
}

func (s strictServer) FinishWebAuthMFAReplace(ctx context.Context, request managementv1.FinishWebAuthMFAReplaceRequestObject) (managementv1.FinishWebAuthMFAReplaceResponseObject, error) {
	credentials, err := s.finishAccountMFA(ctx, request.Body, s.browserAuth.FinishReplace)
	if err != nil {
		return nil, err
	}
	return managementv1.FinishWebAuthMFAReplace200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) DisableWebAuthMFA(ctx context.Context, request managementv1.DisableWebAuthMFARequestObject) (managementv1.DisableWebAuthMFAResponseObject, error) {
	if request.Body == nil || !bool(request.Body.Confirm) {
		return nil, usercmd.ErrInvalid
	}
	password, ok := requiredPointer(request.Body.Password, 1, 4096)
	if !ok {
		return nil, usercmd.ErrInvalid
	}
	passwordBytes := []byte(password)
	defer zero(passwordBytes)
	credentials, err := s.browserAuth.Disable(ctx, webAuthFromContext(ctx).access, passwordBytes, true)
	if err != nil {
		return nil, err
	}
	return managementv1.DisableWebAuthMFA200JSONResponse(credentialsDTO(credentials)), nil
}

func (s strictServer) ListWebAuthSessions(ctx context.Context, request managementv1.ListWebAuthSessionsRequestObject) (managementv1.ListWebAuthSessionsResponseObject, error) {
	userID, afterID := pointerValue(request.Params.UserID), pointerValue(request.Params.AfterID)
	if !boundedString(userID, 0, 256) || !boundedString(afterID, 0, 256) {
		return nil, usercmd.ErrInvalid
	}
	limit := 20
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	if limit < 1 || limit > usercmd.MaxPageSize {
		return nil, usercmd.ErrInvalid
	}
	page, err := s.browserAuth.ListWebAuthSessions(ctx, webAuthFromContext(ctx).access, userID, usercmd.PageRequest{AfterID: afterID, Limit: limit})
	if err != nil {
		return nil, err
	}
	return managementv1.ListWebAuthSessions200JSONResponse(sessionPageDTO(page)), nil
}

func (s strictServer) RevokeWebAuthSession(ctx context.Context, request managementv1.RevokeWebAuthSessionRequestObject) (managementv1.RevokeWebAuthSessionResponseObject, error) {
	if request.Body == nil || !boundedString(request.WebAuthSessionID, 1, 256) {
		return nil, usercmd.ErrInvalid
	}
	if err := s.browserAuth.RevokeWebAuthSession(ctx, webAuthFromContext(ctx).access, request.WebAuthSessionID, request.Body.ConfirmCurrent); err != nil {
		return nil, err
	}
	return managementv1.RevokeWebAuthSession204Response{}, nil
}

type finishMFAFunc func(context.Context, string, string, string, string, []byte) (browserauth.Credentials, error)

func (s strictServer) finishAccountMFA(ctx context.Context, body *managementv1.WebAuthMFAFinishRequest, finish finishMFAFunc) (browserauth.Credentials, error) {
	if body == nil || !boundedString(body.TransactionID, 1, 256) {
		return browserauth.Credentials{}, usercmd.ErrInvalid
	}
	browser, browserOK := requiredPointer(body.BrowserBinding, 16, 256)
	response, responseOK := requiredPointer(body.CredentialResponse, 2, 131072)
	if !browserOK || !responseOK {
		return browserauth.Credentials{}, usercmd.ErrInvalid
	}
	authority := webAuthFromContext(ctx)
	return finish(ctx, authority.access, body.TransactionID, browser, authority.csrf, []byte(response))
}

func credentialsDTO(credentials browserauth.Credentials) managementv1.WebAuthCredentials {
	return managementv1.WebAuthCredentials{
		AccessToken: credentials.AccessToken, RefreshToken: credentials.WebAuthRefreshToken, CSRFToken: credentials.CSRFToken,
		AccessExpiresAt: credentials.AccessExpiresAt, RefreshExpiresAt: credentials.RefreshExpiresAt,
		Assurance: managementv1.WebAuthAssurance(credentials.Assurance),
	}
}

func principalDTO(principal browserauth.Principal) managementv1.WebAuthPrincipal {
	return managementv1.WebAuthPrincipal{
		UserID: principal.User.ID, Username: principal.User.Username, DisplayName: principal.User.DisplayName,
		Role: managementv1.WebAuthRole(principal.User.Role), Assurance: managementv1.WebAuthAssurance(principal.Assurance),
		AccessExpiresAt: principal.AccessExpiresAt, MFAEnabled: principal.MFAEnabled, MFAVersion: principal.MFAVersion,
		UserVersion: principal.User.Version, CredentialVersion: principal.User.Credential.Version,
		WebAuthSessionID: principal.WebAuthSessionID, WebAuthSessionVersion: principal.WebAuthSessionVersion,
	}
}

func continuationDTO(start browserauth.CeremonyStart) (managementv1.WebAuthMFAContinuation, error) {
	data, err := json.Marshal(start.Options)
	if err != nil {
		return managementv1.WebAuthMFAContinuation{}, err
	}
	var options map[string]interface{}
	if err := json.Unmarshal(data, &options); err != nil {
		return managementv1.WebAuthMFAContinuation{}, err
	}
	return managementv1.WebAuthMFAContinuation{TransactionID: start.Transaction, ExpiresAt: start.ExpiresAt, PublicKeyOptions: options}, nil
}

func sessionPageDTO(page usercmd.WebAuthSessionPage) managementv1.WebAuthSessionPage {
	sessions := make([]managementv1.WebAuthSessionSummary, 0, len(page.Sessions))
	for _, session := range page.Sessions {
		mapped := managementv1.WebAuthSessionSummary{
			WebAuthSessionID: session.ID, Assurance: managementv1.WebAuthAssurance(session.Assurance),
			CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, RefreshExpiresAt: session.ExpiresAt, Version: session.Version,
		}
		if !session.RevokedAt.IsZero() {
			mapped.RevokedAt = &session.RevokedAt
		}
		if session.DeviceLabel != "" {
			mapped.DeviceLabel = &session.DeviceLabel
		}
		if session.ConnectionPeer != "" {
			mapped.ConnectionPeer = &session.ConnectionPeer
		}
		sessions = append(sessions, mapped)
	}
	result := managementv1.WebAuthSessionPage{Sessions: sessions}
	if page.NextAfterID != "" {
		result.NextAfterID = &page.NextAfterID
	}
	return result
}

func requiredPointer(value *string, minLength, maxLength int) (string, bool) {
	if value == nil || !boundedString(*value, minLength, maxLength) {
		return "", false
	}
	return *value, true
}

func optionalPointer(value *string, maxLength int) bool {
	return value == nil || boundedString(*value, 0, maxLength)
}

func pointerValue[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func boundedString(value string, minLength, maxLength int) bool {
	length := len(value)
	return length >= minLength && length <= maxLength && (length == 0 || strings.TrimSpace(value) != "")
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
