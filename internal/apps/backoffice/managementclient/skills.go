package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) Skills(ctx context.Context, auth Auth) (managementv1.SkillInventory, error) {
	response, err := c.generated.ListSkillsWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillInventory{}, responseError(response.StatusCode())
}

func (c *Client) ReviewSkillInstall(ctx context.Context, auth Auth, request managementv1.SkillInstallReviewRequest) (managementv1.SkillCandidate, error) {
	response, err := c.generated.ReviewSkillInstallWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillCandidate{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillCandidate{}, responseError(response.StatusCode())
}

func (c *Client) ReviewSkillUpdate(ctx context.Context, auth Auth, id string, request managementv1.SkillObservedStateRequest) (managementv1.SkillCandidate, error) {
	response, err := c.generated.ReviewSkillUpdateWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillCandidate{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillCandidate{}, responseError(response.StatusCode())
}

func (c *Client) AcceptSkillReview(ctx context.Context, auth Auth, request managementv1.SkillAcceptRequest) (managementv1.SkillMutationOutcome, error) {
	response, err := c.generated.AcceptSkillReviewWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillMutationOutcome{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillMutationOutcome{}, responseError(response.StatusCode())
}

func (c *Client) MutateSkill(ctx context.Context, auth Auth, id string, request managementv1.SkillMutationRequest) (managementv1.SkillMutationOutcome, error) {
	response, err := c.generated.MutateSkillWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillMutationOutcome{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillMutationOutcome{}, responseError(response.StatusCode())
}

func (c *Client) RefreshSkills(ctx context.Context, auth Auth, request managementv1.SkillObservedStateRequest) (managementv1.SkillMutationOutcome, error) {
	response, err := c.generated.RefreshSkillsWithResponse(ctx, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillMutationOutcome{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillMutationOutcome{}, responseError(response.StatusCode())
}

func (c *Client) RetrySkillPublication(ctx context.Context, auth Auth) (managementv1.SkillMutationOutcome, error) {
	response, err := c.generated.RetrySkillPublicationWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.SkillMutationOutcome{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.SkillMutationOutcome{}, responseError(response.StatusCode())
}
