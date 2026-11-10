package managementhttp

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListMCPConnections(ctx context.Context, _ managementv1.ListMCPConnectionsRequestObject) (managementv1.ListMCPConnectionsResponseObject, error) {
	if s.mcp == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	items, err := s.mcp.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	providers, err := s.mcp.ProviderIDs(ctx)
	if err != nil {
		return nil, err
	}
	result := managementv1.MCPInventory{ProviderIds: providers, Items: make([]managementv1.MCPItem, 0, len(items))}
	for _, item := range items {
		dto := mcpItemDTO(item)
		attempt, found, attemptErr := s.mcp.CurrentAttempt(ctx, item.Connection.ID, mcpAuthority(ctx))
		if attemptErr != nil && attemptErr != mcpcmd.ErrNotFound {
			return nil, attemptErr
		}
		if found {
			value := mcpAttemptDTO(attempt)
			dto.ActiveAttempt = &value
		}
		result.Items = append(result.Items, dto)
	}
	return managementv1.ListMCPConnections200JSONResponse(result), nil
}

func (s strictServer) OnboardMCPBrowserAuthorization(ctx context.Context, request managementv1.OnboardMCPBrowserAuthorizationRequestObject) (managementv1.OnboardMCPBrowserAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	creation, err := mcpCreate(request.Body.Creation, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	begin := mcpBegin(request.Body.Authorization, "", mcpAuthority(ctx))
	item, started, startErr := s.mcp.CreateAndBeginBrowser(ctx, creation, begin)
	if startErr != nil && item.Connection.ID == "" {
		return nil, startErr
	}
	result := managementv1.MCPBrowserOnboardResult{Item: mcpItemDTO(item)}
	if startErr != nil {
		value := mcpStartError(startErr)
		result.StartError = &value
	} else {
		value := mcpBrowserStartDTO(started)
		result.Start = &value
	}
	return managementv1.OnboardMCPBrowserAuthorization200JSONResponse(result), nil
}
func (s strictServer) OnboardMCPDeviceAuthorization(ctx context.Context, request managementv1.OnboardMCPDeviceAuthorizationRequestObject) (managementv1.OnboardMCPDeviceAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	creation, err := mcpCreate(request.Body.Creation, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	item, started, startErr := s.mcp.CreateAndBeginDevice(ctx, creation, mcpBegin(request.Body.Authorization, "", mcpAuthority(ctx)))
	if startErr != nil && item.Connection.ID == "" {
		return nil, startErr
	}
	result := managementv1.MCPDeviceOnboardResult{Item: mcpItemDTO(item)}
	if startErr != nil {
		value := managementv1.MCPDeviceOnboardResultStartError(mcpStartError(startErr))
		result.StartError = &value
	} else {
		value := mcpDeviceStartDTO(started)
		result.Start = &value
	}
	return managementv1.OnboardMCPDeviceAuthorization200JSONResponse(result), nil
}
func (s strictServer) BeginMCPBrowserAuthorization(ctx context.Context, request managementv1.BeginMCPBrowserAuthorizationRequestObject) (managementv1.BeginMCPBrowserAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	started, err := s.mcp.BeginBrowser(ctx, mcpBegin(*request.Body, request.ConnectionID, mcpAuthority(ctx)))
	if err != nil {
		return nil, err
	}
	return managementv1.BeginMCPBrowserAuthorization200JSONResponse(mcpBrowserStartDTO(started)), nil
}
func (s strictServer) BeginMCPDeviceAuthorization(ctx context.Context, request managementv1.BeginMCPDeviceAuthorizationRequestObject) (managementv1.BeginMCPDeviceAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	started, err := s.mcp.BeginDevice(ctx, mcpBegin(*request.Body, request.ConnectionID, mcpAuthority(ctx)))
	if err != nil {
		return nil, err
	}
	return managementv1.BeginMCPDeviceAuthorization200JSONResponse(mcpDeviceStartDTO(started)), nil
}
func (s strictServer) CompleteMCPBrowserAuthorization(ctx context.Context, request managementv1.CompleteMCPBrowserAuthorizationRequestObject) (managementv1.CompleteMCPBrowserAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	item, err := s.mcp.CompleteBrowser(ctx, mcpcmd.BrowserCallback{State: stringValue(request.Body.State), Code: stringValue(request.Body.Code), Issuer: request.Body.Issuer, Denied: request.Body.Denied, Authority: mcpAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.CompleteMCPBrowserAuthorization200JSONResponse(mcpItemDTO(item)), nil
}
func (s strictServer) GetMCPDeviceAuthorization(ctx context.Context, request managementv1.GetMCPDeviceAuthorizationRequestObject) (managementv1.GetMCPDeviceAuthorizationResponseObject, error) {
	if s.mcp == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	device, err := s.mcp.Device(ctx, request.AttemptID, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetMCPDeviceAuthorization200JSONResponse(mcpDeviceStatusDTO(device)), nil
}
func (s strictServer) CancelMCPAuthorization(ctx context.Context, request managementv1.CancelMCPAuthorizationRequestObject) (managementv1.CancelMCPAuthorizationResponseObject, error) {
	if s.mcp == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	if err := s.mcp.Cancel(ctx, request.AttemptID, mcpAuthority(ctx)); err != nil {
		return nil, err
	}
	return managementv1.CancelMCPAuthorization204Response{}, nil
}
func (s strictServer) DisconnectMCPAuthorization(ctx context.Context, request managementv1.DisconnectMCPAuthorizationRequestObject) (managementv1.DisconnectMCPAuthorizationResponseObject, error) {
	if s.mcp == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	if err := s.mcp.Disconnect(ctx, request.ConnectionID, mcpAuthority(ctx)); err != nil {
		return nil, err
	}
	return managementv1.DisconnectMCPAuthorization204Response{}, nil
}
func (s strictServer) RetryMCPAuthorization(ctx context.Context, request managementv1.RetryMCPAuthorizationRequestObject) (managementv1.RetryMCPAuthorizationResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	item, err := s.mcp.RetryAuthorization(ctx, mcpcmd.SelectAuthorization{ConnectionID: request.ConnectionID, ExpectedRevisionID: request.Body.ExpectedRevisionID, Authority: mcpAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.RetryMCPAuthorization200JSONResponse(mcpItemDTO(item)), nil
}

func mcpBegin(input managementv1.MCPAuthorizationInput, id string, authority mcpcmd.Authority) mcpcmd.BeginAuthorization {
	return mcpcmd.BeginAuthorization{ConnectionID: id, Scopes: input.Scopes, ClientID: input.ClientID, ClientAuthMethod: string(input.ClientAuthMethod), ClientSecret: stringValue(input.ClientSecret), Authority: authority}
}
func mcpBrowserStartDTO(input mcpcmd.BrowserAuthorization) managementv1.MCPBrowserAuthorizationStart {
	return managementv1.MCPBrowserAuthorizationStart{AttemptID: input.ID, ConnectionID: input.ConnectionID, AuthorizationURL: input.AuthorizationURL, ExpiresAt: input.ExpiresAt}
}
func mcpDeviceStartDTO(input mcpcmd.DeviceAuthorization) managementv1.MCPDeviceAuthorizationStart {
	return managementv1.MCPDeviceAuthorizationStart{AttemptID: input.ID, ConnectionID: input.ConnectionID, VerificationURI: input.VerificationURI, VerificationURIComplete: stringPtr(input.VerificationURIComplete), UserCode: input.UserCode, ExpiresAt: input.ExpiresAt, Status: managementv1.MCPDeviceAuthorizationStartStatus(input.Status)}
}
func mcpDeviceStatusDTO(input mcpcmd.DeviceAuthorization) managementv1.MCPDeviceAuthorizationStatus {
	return managementv1.MCPDeviceAuthorizationStatus{AttemptID: input.ID, ConnectionID: input.ConnectionID, ExpiresAt: input.ExpiresAt, Status: managementv1.MCPDeviceAuthorizationStatusStatus(input.Status)}
}
func mcpAttemptDTO(input mcpcmd.AuthorizationAttempt) managementv1.MCPAuthorizationAttempt {
	return managementv1.MCPAuthorizationAttempt{AttemptID: input.ID, ConnectionID: input.ConnectionID, ExpiresAt: input.ExpiresAt, Device: input.Device}
}
func mcpStartError(err error) managementv1.MCPBrowserOnboardResultStartError {
	switch {
	case errors.Is(err, mcpcmd.ErrInvalid):
		return managementv1.MCPBrowserOnboardResultStartErrorInvalid
	case errors.Is(err, mcpcmd.ErrForbidden):
		return managementv1.MCPBrowserOnboardResultStartErrorForbidden
	case err == mcpcmd.ErrConflict:
		return managementv1.MCPBrowserOnboardResultStartErrorConflict
	case err == mcpcmd.ErrAuthRequired:
		return managementv1.MCPBrowserOnboardResultStartErrorAuthRequired
	case err == mcpcmd.ErrDisconnected:
		return managementv1.MCPBrowserOnboardResultStartErrorDisconnected
	default:
		return managementv1.MCPBrowserOnboardResultStartErrorUnavailable
	}
}

func (s strictServer) CreateMCPConnection(ctx context.Context, request managementv1.CreateMCPConnectionRequestObject) (managementv1.CreateMCPConnectionResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	command, err := mcpCreate(*request.Body, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	item, err := s.mcp.Create(ctx, command)
	if err != nil {
		return nil, err
	}
	return managementv1.CreateMCPConnection200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) ProbeMCPConnection(ctx context.Context, request managementv1.ProbeMCPConnectionRequestObject) (managementv1.ProbeMCPConnectionResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	command, err := mcpCreate(*request.Body, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	item, err := s.mcp.Probe(ctx, command)
	if err != nil {
		return nil, err
	}
	return managementv1.ProbeMCPConnection200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) UpdateMCPConnection(ctx context.Context, request managementv1.UpdateMCPConnectionRequestObject) (managementv1.UpdateMCPConnectionResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	if err := s.mutableMCP(ctx, request.ConnectionID); err != nil {
		return nil, err
	}
	command, err := mcpUpdate(request.ConnectionID, *request.Body, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	item, err := s.mcp.Update(ctx, command)
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateMCPConnection200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) ProbeMCPConnectionUpdate(ctx context.Context, request managementv1.ProbeMCPConnectionUpdateRequestObject) (managementv1.ProbeMCPConnectionUpdateResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	if err := s.mutableMCP(ctx, request.ConnectionID); err != nil {
		return nil, err
	}
	command, err := mcpUpdate(request.ConnectionID, *request.Body, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	item, err := s.mcp.ProbeUpdate(ctx, command)
	if err != nil {
		return nil, err
	}
	return managementv1.ProbeMCPConnectionUpdate200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) SelectMCPConnection(ctx context.Context, request managementv1.SelectMCPConnectionRequestObject) (managementv1.SelectMCPConnectionResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	if err := s.mutableMCP(ctx, request.ConnectionID); err != nil {
		return nil, err
	}
	item, err := s.mcp.SetEnabled(ctx, mcpcmd.ChangeSelection{ConnectionID: request.ConnectionID, ExpectedVersion: request.Body.ExpectedVersion, Enabled: request.Body.Enabled, Authority: mcpAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.SelectMCPConnection200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) DeleteMCPConnection(ctx context.Context, request managementv1.DeleteMCPConnectionRequestObject) (managementv1.DeleteMCPConnectionResponseObject, error) {
	if s.mcp == nil || request.Body == nil {
		return nil, mcpcmd.ErrInvalid
	}
	if err := s.mutableMCP(ctx, request.ConnectionID); err != nil {
		return nil, err
	}
	item, err := s.mcp.Delete(ctx, mcpcmd.ChangeSelection{ConnectionID: request.ConnectionID, ExpectedVersion: request.Body.ExpectedVersion, Enabled: false, Authority: mcpAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.DeleteMCPConnection200JSONResponse(mcpItemDTO(item)), nil
}

func (s strictServer) mutableMCP(ctx context.Context, id string) error {
	items, err := s.mcp.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Connection.ID != id {
			continue
		}
		if item.Connection.Source != mcpcmd.SourceManaged {
			return mcpcmd.ErrForbidden
		}
		if item.Connection.Deleted {
			return mcpcmd.ErrConflict
		}
		return nil
	}
	return mcpcmd.ErrNotFound
}

func (s strictServer) DiagnoseMCPConnection(ctx context.Context, request managementv1.DiagnoseMCPConnectionRequestObject) (managementv1.DiagnoseMCPConnectionResponseObject, error) {
	if s.mcp == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	result, err := s.mcp.DiagnoseCurrent(ctx, request.ConnectionID, mcpAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.DiagnoseMCPConnection200JSONResponse(mcpDiagnosticDTO(result)), nil
}

func mcpAuthority(ctx context.Context) mcpcmd.Authority {
	p := webAuthFromContext(ctx).principal
	return mcpcmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}

func mcpCreate(input managementv1.MCPCreateRequest, authority mcpcmd.Authority) (mcpcmd.CreateDefinition, error) {
	definition, err := mcpDefinition(input.Definition)
	if err != nil {
		return mcpcmd.CreateDefinition{}, err
	}
	return mcpcmd.CreateDefinition{PublicID: input.PublicID, Definition: definition, Values: mcpEdits(input.Env, input.Headers), Enabled: input.Enabled, Authority: authority}, nil
}

func mcpUpdate(id string, input managementv1.MCPUpdateRequest, authority mcpcmd.Authority) (mcpcmd.UpdateDefinition, error) {
	definition, err := mcpDefinition(input.Definition)
	if err != nil {
		return mcpcmd.UpdateDefinition{}, err
	}
	return mcpcmd.UpdateDefinition{ConnectionID: id, ExpectedVersion: input.ExpectedVersion, Definition: definition, Values: mcpEdits(input.Env, input.Headers), Enabled: input.Enabled, Authority: authority}, nil
}

func mcpDefinition(input managementv1.MCPDefinition) (mcpcmd.Definition, error) {
	if input.ConfigRevision != nil {
		return mcpcmd.Definition{}, mcpcmd.ErrInvalid
	}
	result := mcpcmd.Definition{Transport: mcpcmd.Transport(input.Transport), Command: stringValue(input.Command), Args: cloneStrings(input.Args), Directory: stringValue(input.Directory), URL: stringValue(input.URL), Targets: mcpcmd.Targets{All: input.Targets.All, Providers: input.Targets.Providers}, OAuth: boolValue(input.Oauth), ConfigRevision: stringValue(input.ConfigRevision), Scopes: cloneStrings(input.Scopes), Env: mcpBindings(input.Env), Headers: mcpBindings(input.Headers)}
	if result.Transport != mcpcmd.TransportStdio && result.Transport != mcpcmd.TransportHTTP && result.Transport != mcpcmd.TransportSSE {
		return mcpcmd.Definition{}, mcpcmd.ErrInvalid
	}
	return result, nil
}

func mcpBindings(input *map[string]managementv1.MCPValueBinding) map[string]mcpcmd.ValueBinding {
	if input == nil {
		return nil
	}
	result := make(map[string]mcpcmd.ValueBinding, len(*input))
	for k, v := range *input {
		result[k] = mcpcmd.ValueBinding{Kind: mcpcmd.ValueKind(v.Kind), Value: stringValue(v.Value)}
	}
	return result
}

func mcpEdits(env, headers *managementv1.MCPValueEdits) mcpcmd.ValueEdits {
	return mcpcmd.ValueEdits{Env: mcpEditMap(env), Headers: mcpEditMap(headers)}
}
func mcpEditMap(input *managementv1.MCPValueEdits) map[string]mcpcmd.ValueEdit {
	if input == nil {
		return nil
	}
	result := make(map[string]mcpcmd.ValueEdit, len(*input))
	for k, v := range *input {
		result[k] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueOperation(v.Operation), Kind: mcpcmd.ValueKind(v.Kind), Value: stringValue(v.Value)}
	}
	return result
}

func mcpItemDTO(item mcpcmd.Item) managementv1.MCPItem {
	result := managementv1.MCPItem{ConnectionID: item.Connection.ID, PublicID: item.Connection.PublicID, Source: managementv1.MCPItemSource(item.Connection.Source), CurrentRevisionID: item.Connection.CurrentRevisionID, Enabled: item.Connection.Enabled, Deleted: item.Connection.Deleted, Version: item.Connection.Version, PublishedVersion: item.Connection.PublishedVersion, Definition: mcpDefinitionDTO(item.Definition), Status: managementv1.MCPItemStatus(item.Status), ToolCount: int32(item.ToolCount), Authorization: string(item.Authorization)}
	if item.Recovery != "" {
		v := managementv1.MCPItemRecovery(item.Recovery)
		result.Recovery = &v
	}
	if item.LastDiagnostic != nil {
		v := mcpDiagnosticDTO(*item.LastDiagnostic)
		result.LastDiagnostic = &v
	}
	return result
}

func mcpDefinitionDTO(input mcpcmd.Definition) managementv1.MCPDefinition {
	result := managementv1.MCPDefinition{Transport: managementv1.MCPDefinitionTransport(input.Transport), Targets: managementv1.MCPTargets{All: input.Targets.All, Providers: input.Targets.Providers}, Command: stringPtr(input.Command), Args: slicePtr(input.Args), Directory: stringPtr(input.Directory), URL: stringPtr(input.URL), Env: mcpBindingsDTO(input.Env), Headers: mcpBindingsDTO(input.Headers), Oauth: boolPtr(input.OAuth), ConfigRevision: stringPtr(input.ConfigRevision), Scopes: slicePtr(input.Scopes)}
	return result
}
func mcpBindingsDTO(input map[string]mcpcmd.ValueBinding) *map[string]managementv1.MCPValueBinding {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]managementv1.MCPValueBinding, len(input))
	for k, v := range input {
		result[k] = managementv1.MCPValueBinding{Kind: managementv1.MCPValueBindingKind(v.Kind), Value: stringPtr(v.Value)}
	}
	return &result
}
func mcpDiagnosticDTO(input mcpcmd.DiagnosticResult) managementv1.MCPDiagnostic {
	return managementv1.MCPDiagnostic{Outcome: managementv1.MCPDiagnosticOutcome(input.Outcome), ToolCount: int32(input.ToolCount), CheckedAt: input.CheckedAt}
}
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func slicePtr(v []string) *[]string {
	if len(v) == 0 {
		return nil
	}
	return &v
}
func boolPtr(v bool) *bool   { return &v }
func boolValue(v *bool) bool { return v != nil && *v }
