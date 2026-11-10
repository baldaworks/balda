package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListSkills(ctx context.Context, _ managementv1.ListSkillsRequestObject) (managementv1.ListSkillsResponseObject, error) {
	inventory, err := s.skills.Inventory(ctx, skillAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.ListSkills200JSONResponse(skillInventoryDTO(inventory)), nil
}

func (s strictServer) ReviewSkillInstall(ctx context.Context, request managementv1.ReviewSkillInstallRequestObject) (managementv1.ReviewSkillInstallResponseObject, error) {
	if request.Body == nil {
		return nil, skillcmd.ErrInvalid
	}
	candidate, err := s.skills.ReviewInstall(ctx, skillcmd.InstallReview{
		Source: request.Body.Source, Ref: request.Body.Ref, Path: request.Body.Path,
		ExpectedState: request.Body.ExpectedState, Authority: skillAuthority(ctx),
	})
	if err != nil {
		return nil, err
	}
	return managementv1.ReviewSkillInstall200JSONResponse(skillCandidateDTO(candidate)), nil
}

func (s strictServer) ReviewSkillUpdate(ctx context.Context, request managementv1.ReviewSkillUpdateRequestObject) (managementv1.ReviewSkillUpdateResponseObject, error) {
	if request.Body == nil {
		return nil, skillcmd.ErrInvalid
	}
	candidate, err := s.skills.ReviewUpdate(ctx, skillMutation(request.SkillID, request.Body.ExpectedState, ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.ReviewSkillUpdate200JSONResponse(skillCandidateDTO(candidate)), nil
}

func (s strictServer) AcceptSkillReview(ctx context.Context, request managementv1.AcceptSkillReviewRequestObject) (managementv1.AcceptSkillReviewResponseObject, error) {
	if request.Body == nil {
		return nil, skillcmd.ErrInvalid
	}
	outcome, err := s.skills.Accept(ctx, request.Body.CandidateHandle, skillAuthority(ctx))
	if err != nil && (!outcome.Accepted || !outcome.Pending) {
		return nil, err
	}
	return managementv1.AcceptSkillReview200JSONResponse(skillOutcomeDTO(outcome)), nil
}

func (s strictServer) MutateSkill(ctx context.Context, request managementv1.MutateSkillRequestObject) (managementv1.MutateSkillResponseObject, error) {
	if request.Body == nil {
		return nil, skillcmd.ErrInvalid
	}
	operation := skillcmd.Operation(request.Body.Operation)
	if operation != skillcmd.OperationEnable && operation != skillcmd.OperationDisable && operation != skillcmd.OperationRemove {
		return nil, skillcmd.ErrInvalid
	}
	if (operation == skillcmd.OperationDisable || operation == skillcmd.OperationRemove) && !request.Body.Confirm {
		return nil, skillcmd.ErrInvalid
	}
	outcome, err := s.skills.Change(ctx, operation, skillMutation(request.SkillID, request.Body.ExpectedState, ctx))
	if err != nil && (!outcome.Accepted || !outcome.Pending) {
		return nil, err
	}
	return managementv1.MutateSkill200JSONResponse(skillOutcomeDTO(outcome)), nil
}

func (s strictServer) RefreshSkills(ctx context.Context, request managementv1.RefreshSkillsRequestObject) (managementv1.RefreshSkillsResponseObject, error) {
	if request.Body == nil {
		return nil, skillcmd.ErrInvalid
	}
	outcome, err := s.skills.Refresh(ctx, skillcmd.Mutation{ExpectedState: request.Body.ExpectedState, Authority: skillAuthority(ctx)})
	if err != nil && (!outcome.Accepted || !outcome.Pending) {
		return nil, err
	}
	return managementv1.RefreshSkills200JSONResponse(skillOutcomeDTO(outcome)), nil
}

func (s strictServer) RetrySkillPublication(ctx context.Context, _ managementv1.RetrySkillPublicationRequestObject) (managementv1.RetrySkillPublicationResponseObject, error) {
	outcome, err := s.skills.RetryPublication(ctx, skillAuthority(ctx))
	if err != nil && (!outcome.Accepted || !outcome.Pending) {
		return nil, err
	}
	return managementv1.RetrySkillPublication200JSONResponse(skillOutcomeDTO(outcome)), nil
}

func skillAuthority(ctx context.Context) skillcmd.Authority {
	p := webAuthFromContext(ctx).principal
	return skillcmd.NewBrowserAuthority(
		p.User.ID, p.User.Version, p.User.Credential.Version, p.MFAVersion,
		p.WebAuthSessionID, p.WebAuthSessionVersion, time.Now().UTC(),
	)
}

func skillMutation(id, expectedState string, ctx context.Context) skillcmd.Mutation {
	return skillcmd.Mutation{
		ID:            runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: id},
		ExpectedState: expectedState, Authority: skillAuthority(ctx),
	}
}

func skillInventoryDTO(inventory skillcmd.Inventory) managementv1.SkillInventory {
	result := managementv1.SkillInventory{
		Diagnostics: append([]string{}, inventory.Diagnostics...), Items: make([]managementv1.SkillEntry, 0, len(inventory.Entries)),
		ExpectedState: inventory.ExpectedState, Generation: inventory.Generation, PublicationPending: inventory.Pending,
	}
	for _, entry := range inventory.Entries {
		result.Items = append(result.Items, skillEntryDTO(entry))
	}
	return result
}

func skillCandidateDTO(candidate skillcmd.Candidate) managementv1.SkillCandidate {
	return managementv1.SkillCandidate{
		CandidateHandle: candidate.Handle, Item: skillEntryDTO(candidate.Entry), SkillPath: candidate.SkillPath,
		ResolvedCommit: candidate.ResolvedCommit, ExpectedState: candidate.ExpectedState, ExpiresAt: candidate.ExpiresAt,
	}
}

func skillEntryDTO(entry skillcmd.Entry) managementv1.SkillEntry {
	return managementv1.SkillEntry{
		SourceKind: string(entry.ID.Source.Kind), SourceName: entry.ID.Source.Name,
		CurrentRevision: string(entry.CurrentRevision), Revision: string(entry.Revision), Name: entry.Name,
		Description: entry.Description, Ownership: managementv1.SkillOwnership(entry.Ownership), SourceLabel: entry.SourceLabel,
		PluginID: entry.PluginID, Enabled: entry.Enabled, Pending: entry.Pending,
	}
}

func skillOutcomeDTO(outcome skillcmd.Outcome) managementv1.SkillMutationOutcome {
	result := managementv1.SkillMutationOutcome{Accepted: outcome.Accepted, PublicationPending: outcome.Pending, Generation: outcome.Generation}
	if outcome.Entry != nil {
		entry := skillEntryDTO(*outcome.Entry)
		result.Item = &entry
	}
	return result
}
