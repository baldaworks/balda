package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestSkillBrowserDoesNotReflectSourceOrRetryAcceptedMutation(t *testing.T) {
	service := &skillEdgeService{candidate: skillCandidateFixture(), outcome: managementv1.SkillMutationOutcome{Accepted: true, PublicationPending: true}}
	app := newTestApp(t, service)
	reviewRequest := mutationRequest("/balda/backoffice/skills/reviews/install", url.Values{
		"source": {"https://secret@example.invalid/private.git"}, "ref": {"main"}, "path": {"skills/calendar"}, "expected_state": {"state-1"},
	})
	reviewRequest.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	review := httptest.NewRecorder()
	app.Handler().ServeHTTP(review, reviewRequest)
	require.Equal(t, http.StatusOK, review.Code)
	require.NotContains(t, review.Body.String(), "secret@example")
	require.Equal(t, "https://secret@example.invalid/private.git", service.install.Source)

	acceptRequest := mutationRequest("/balda/backoffice/skills/reviews/accept", url.Values{"candidate_handle": {"opaque-handle"}})
	acceptRequest.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	accepted := httptest.NewRecorder()
	app.Handler().ServeHTTP(accepted, acceptRequest)
	require.Equal(t, http.StatusServiceUnavailable, accepted.Code)
	require.Equal(t, 1, service.acceptCalls)
}

func TestSkillBrowserRequiresDestructiveConfirmation(t *testing.T) {
	service := &skillEdgeService{}
	app := newTestApp(t, service)
	request := mutationRequest("/balda/backoffice/skills/calendar/lifecycle", url.Values{
		"operation": {string(managementv1.SkillLifecycleOperationRemove)}, "expected_state": {"state-1"},
	})
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, 0, service.mutateCalls)
}

type skillEdgeService struct {
	fakeService
	candidate   managementv1.SkillCandidate
	install     managementv1.SkillInstallReviewRequest
	outcome     managementv1.SkillMutationOutcome
	acceptCalls int
	mutateCalls int
}

func (*skillEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (*skillEdgeService) Skills(context.Context, managementclient.Auth) (managementv1.SkillInventory, error) {
	return managementv1.SkillInventory{Diagnostics: []string{}, Items: []managementv1.SkillEntry{}}, nil
}
func (s *skillEdgeService) ReviewSkillInstall(_ context.Context, _ managementclient.Auth, request managementv1.SkillInstallReviewRequest) (managementv1.SkillCandidate, error) {
	s.install = request
	return s.candidate, nil
}
func (s *skillEdgeService) ReviewSkillUpdate(context.Context, managementclient.Auth, string, managementv1.SkillObservedStateRequest) (managementv1.SkillCandidate, error) {
	return s.candidate, nil
}
func (s *skillEdgeService) AcceptSkillReview(context.Context, managementclient.Auth, managementv1.SkillAcceptRequest) (managementv1.SkillMutationOutcome, error) {
	s.acceptCalls++
	return s.outcome, nil
}
func (s *skillEdgeService) MutateSkill(context.Context, managementclient.Auth, string, managementv1.SkillMutationRequest) (managementv1.SkillMutationOutcome, error) {
	s.mutateCalls++
	return s.outcome, nil
}
func (s *skillEdgeService) RefreshSkills(context.Context, managementclient.Auth, managementv1.SkillObservedStateRequest) (managementv1.SkillMutationOutcome, error) {
	return s.outcome, nil
}
func (s *skillEdgeService) RetrySkillPublication(context.Context, managementclient.Auth) (managementv1.SkillMutationOutcome, error) {
	return s.outcome, nil
}

func skillCandidateFixture() managementv1.SkillCandidate {
	return managementv1.SkillCandidate{
		CandidateHandle: "opaque-handle", ExpectedState: "state-1", ExpiresAt: time.Now().Add(time.Minute),
		Item: managementv1.SkillEntry{SourceKind: "user-skill", SourceName: "calendar", Name: "calendar", Ownership: managementv1.SkillOwnershipManaged},
	}
}

var _ Service = (*skillEdgeService)(nil)
var _ SkillService = (*skillEdgeService)(nil)
