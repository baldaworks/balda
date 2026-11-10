package managementhttp

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListPlugins(ctx context.Context, _ managementv1.ListPluginsRequestObject) (managementv1.ListPluginsResponseObject, error) {
	items, err := s.plugins.Inventory(ctx, pluginAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.PluginInventory{Items: make([]managementv1.PluginInstalled, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, pluginInstalledDTO(item))
	}
	return managementv1.ListPlugins200JSONResponse(result), nil
}

func (s strictServer) GetPlugin(ctx context.Context, request managementv1.GetPluginRequestObject) (managementv1.GetPluginResponseObject, error) {
	detail, err := s.plugins.Detail(ctx, request.PluginName, pluginAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetPlugin200JSONResponse(pluginDetailDTO(detail)), nil
}

func (s strictServer) ReviewPluginCandidate(ctx context.Context, request managementv1.ReviewPluginCandidateRequestObject) (managementv1.ReviewPluginCandidateResponseObject, error) {
	marketplace := ""
	if request.Params.Marketplace != nil {
		marketplace = *request.Params.Marketplace
	}
	candidate, err := s.plugins.Candidate(ctx, request.PluginName, marketplace, pluginAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.ReviewPluginCandidate200JSONResponse(pluginCandidateDTO(candidate)), nil
}

func (s strictServer) MutatePlugin(ctx context.Context, request managementv1.MutatePluginRequestObject) (managementv1.MutatePluginResponseObject, error) {
	if request.Body == nil {
		return nil, plugincmd.ErrInvalid
	}
	mutation := plugincmd.PluginMutation{
		Operation: plugincmd.Operation(request.Body.Operation), PluginName: request.PluginName,
		MarketplaceName: request.Body.MarketplaceName, CandidateToken: request.Body.CandidateToken,
		Revision: request.Body.Revision, ExpectedState: request.Body.ExpectedState, Confirm: request.Body.Confirm,
		Authority: pluginAuthority(ctx),
	}
	if mutation.Validate() != nil {
		return nil, plugincmd.ErrInvalid
	}
	outcome, err := s.plugins.MutatePlugin(ctx, mutation)
	if err != nil && (!errors.Is(err, plugincmd.ErrRecoveryPending) || !outcome.Accepted) {
		return nil, err
	}
	return managementv1.MutatePlugin200JSONResponse(pluginOutcomeDTO(outcome)), nil
}

func (s strictServer) ListPluginMarketplaces(ctx context.Context, _ managementv1.ListPluginMarketplacesRequestObject) (managementv1.ListPluginMarketplacesResponseObject, error) {
	items, err := s.plugins.Marketplaces(ctx, pluginAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.PluginMarketplaceInventory{Items: make([]managementv1.PluginMarketplace, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, pluginMarketplaceDTO(item))
	}
	return managementv1.ListPluginMarketplaces200JSONResponse(result), nil
}

func (s strictServer) GetPluginMarketplace(ctx context.Context, request managementv1.GetPluginMarketplaceRequestObject) (managementv1.GetPluginMarketplaceResponseObject, error) {
	detail, err := s.plugins.Marketplace(ctx, request.MarketplaceName, pluginAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetPluginMarketplace200JSONResponse(pluginMarketplaceDetailDTO(detail)), nil
}

func (s strictServer) AddPluginMarketplace(ctx context.Context, request managementv1.AddPluginMarketplaceRequestObject) (managementv1.AddPluginMarketplaceResponseObject, error) {
	if request.Body == nil || request.Body.Sparse == nil {
		return nil, plugincmd.ErrInvalid
	}
	detail, err := s.plugins.AddMarketplace(ctx, plugincmd.MarketplaceAdd{
		Name: request.Body.Name, Source: request.Body.Source, Ref: request.Body.Ref,
		Sparse: append([]string{}, request.Body.Sparse...), Authority: pluginAuthority(ctx),
	})
	if err != nil {
		return nil, err
	}
	return managementv1.AddPluginMarketplace200JSONResponse(pluginMarketplaceDetailDTO(detail)), nil
}

func (s strictServer) RefreshPluginMarketplace(ctx context.Context, request managementv1.RefreshPluginMarketplaceRequestObject) (managementv1.RefreshPluginMarketplaceResponseObject, error) {
	if request.Body == nil {
		return nil, plugincmd.ErrInvalid
	}
	detail, err := s.plugins.RefreshMarketplace(ctx, plugincmd.MarketplaceMutation{
		Name: request.MarketplaceName, ExpectedState: request.Body.ExpectedState,
		Confirm: request.Body.Confirm, Authority: pluginAuthority(ctx),
	})
	if err != nil {
		return nil, err
	}
	return managementv1.RefreshPluginMarketplace200JSONResponse(pluginMarketplaceDetailDTO(detail)), nil
}

func (s strictServer) RemovePluginMarketplace(ctx context.Context, request managementv1.RemovePluginMarketplaceRequestObject) (managementv1.RemovePluginMarketplaceResponseObject, error) {
	if request.Body == nil || !request.Body.Confirm {
		return nil, plugincmd.ErrInvalid
	}
	if err := s.plugins.RemoveMarketplace(ctx, plugincmd.MarketplaceMutation{
		Name: request.MarketplaceName, ExpectedState: request.Body.ExpectedState,
		Confirm: true, Authority: pluginAuthority(ctx),
	}); err != nil {
		return nil, err
	}
	return managementv1.RemovePluginMarketplace204Response{}, nil
}

func pluginAuthority(ctx context.Context) plugincmd.Authority {
	p := webAuthFromContext(ctx).principal
	return plugincmd.Authority{
		UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID,
		WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC(),
	}
}

func pluginInstalledDTO(item plugincmd.InstalledView) managementv1.PluginInstalled {
	return managementv1.PluginInstalled{
		Name: item.Name, Version: item.Version, Description: item.Description, Marketplace: item.Marketplace,
		Origin: item.Origin, Revision: item.Revision, Enabled: item.Enabled, Drifted: item.Drifted, Removed: item.Removed,
		Capabilities: pluginCapabilityDTO(item.Capabilities), Runtime: pluginRuntimeDTO(item.Runtime),
		ExpectedState: item.ExpectedState, OriginAvailable: item.OriginAvailable, RecoveryPending: item.RecoveryPending,
		DeclaresBaldaExtension: item.DeclaresBaldaExtension,
	}
}

func pluginDetailDTO(detail plugincmd.PluginDetail) managementv1.PluginDetail {
	return managementv1.PluginDetail{
		Item: pluginInstalledDTO(detail.InstalledView), Commands: pluginContributionsDTO(detail.Commands),
		Skills: pluginContributionsDTO(detail.Skills), McpServers: pluginContributionsDTO(detail.MCPServers),
		Revisions: pluginRevisionsDTO(detail.Revisions), DiagnosticCodes: append([]string{}, detail.DiagnosticCodes...),
	}
}

func pluginCandidateDTO(candidate plugincmd.CandidateView) managementv1.PluginCandidate {
	return managementv1.PluginCandidate{
		Name: candidate.Name, Version: candidate.Version, Description: candidate.Description,
		Marketplace: candidate.Marketplace, Revision: candidate.Revision, CandidateToken: candidate.CandidateToken,
		ExpectedState: candidate.ExpectedState, DeclaresBaldaExtension: candidate.DeclaresBaldaExtension,
		Capabilities: pluginCapabilityDTO(candidate.Capabilities), DiagnosticCodes: append([]string{}, candidate.DiagnosticCodes...),
	}
}

func pluginMarketplaceDTO(item plugincmd.MarketplaceView) managementv1.PluginMarketplace {
	return managementv1.PluginMarketplace{
		Name: item.Name, Source: item.Source, Kind: item.Kind, Ref: item.Ref, Sparse: append([]string{}, item.Sparse...),
		ExpectedState: item.ExpectedState, LastAttemptAt: pluginTime(item.LastAttemptAt), LastAttemptStatus: item.LastAttemptStatus,
		FailureCode: item.FailureCode, LastSuccessfulAt: pluginTime(item.LastSuccessfulAt),
		ResolvedRevision: item.ResolvedRevision, AvailablePlugins: item.AvailablePlugins,
	}
}

func pluginMarketplaceDetailDTO(detail plugincmd.MarketplaceDetail) managementv1.PluginMarketplaceDetail {
	candidates := make([]managementv1.PluginCandidate, 0, len(detail.Candidates))
	for _, candidate := range detail.Candidates {
		candidates = append(candidates, pluginCandidateDTO(candidate))
	}
	return managementv1.PluginMarketplaceDetail{
		Item: pluginMarketplaceDTO(detail.MarketplaceView), Candidates: candidates,
		DiagnosticCodes: append([]string{}, detail.DiagnosticCodes...),
	}
}

func pluginCapabilityDTO(summary plugincmd.CapabilitySummary) managementv1.PluginCapabilitySummary {
	return managementv1.PluginCapabilitySummary{Commands: summary.Commands, Skills: summary.Skills, McpServers: summary.MCPServers, Diagnostics: summary.Diagnostics}
}

func pluginRuntimeDTO(status plugincmd.RuntimeStatus) managementv1.PluginRuntimeStatus {
	return managementv1.PluginRuntimeStatus{
		RevisionCurrent: status.RevisionCurrent, SnapshotID: status.SnapshotID, SnapshotSequence: status.SnapshotSequence,
		ProjectionLag: status.ProjectionLag, Advertisements: status.Advertisements, Skills: status.Skills,
		SkillAmbiguities: status.SkillAmbiguities, DesiredMcpServers: status.DesiredMCPServers,
		ReadyMcpServers: status.ReadyMCPServers, DegradedMcpServers: status.DegradedMCPServers,
		DiagnosticCodes: append([]string{}, status.DiagnosticCodes...), ProjectionOmissions: append([]string{}, status.ProjectionOmissions...),
	}
}

func pluginContributionsDTO(items []plugincmd.Contribution) []managementv1.PluginContribution {
	result := make([]managementv1.PluginContribution, 0, len(items))
	for _, item := range items {
		result = append(result, managementv1.PluginContribution{Name: item.Name, Description: item.Description, Transport: item.Transport})
	}
	return result
}

func pluginRevisionsDTO(items []plugincmd.RetainedRevision) []managementv1.PluginRetainedRevision {
	result := make([]managementv1.PluginRetainedRevision, 0, len(items))
	for _, item := range items {
		result = append(result, managementv1.PluginRetainedRevision{
			Version: item.Version, Revision: item.Revision, RetainedAt: item.RetainedAt,
			Available: item.Available, Current: item.Current,
		})
	}
	return result
}

func pluginOutcomeDTO(outcome plugincmd.MutationOutcome) managementv1.PluginMutationOutcome {
	return managementv1.PluginMutationOutcome{PluginName: outcome.PluginName, Accepted: outcome.Accepted, RecoveryPending: outcome.RecoveryPending}
}

func pluginTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
