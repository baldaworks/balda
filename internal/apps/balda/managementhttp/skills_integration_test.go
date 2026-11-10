package managementhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientSkillLifecyclePreservesCorePolicy(t *testing.T) {
	fixture := &skillFixture{}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, nil, nil, fixture))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	inventory, err := client.ListSkillsWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Items) != 1 || fixture.inventoryCalls != 1 || fixture.reviewCalls != 0 || fixture.changeCalls != 0 {
		t.Fatalf("inventory = %#v, calls = (%d,%d,%d), %v", inventory, fixture.inventoryCalls, fixture.reviewCalls, fixture.changeCalls, err)
	}

	install := managementv1.SkillInstallReviewRequest{Source: "https://secret@example.invalid/private.git", Ref: "main", Path: "skills/calendar", ExpectedState: "state-1"}
	withoutCSRF, err := client.ReviewSkillInstallWithResponse(t.Context(), install, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden || fixture.reviewCalls != 0 {
		t.Fatalf("review without csrf = %#v, calls = %d, %v", withoutCSRF, fixture.reviewCalls, err)
	}
	review, err := client.ReviewSkillInstallWithResponse(t.Context(), install, write)
	if err != nil || review.JSON200 == nil || review.JSON200.CandidateHandle != "opaque-handle" || fixture.reviewCalls != 1 || !fixture.authority.Browser() {
		t.Fatalf("review = %#v, authority = %#v, calls = %d, %v", review, fixture.authority, fixture.reviewCalls, err)
	}
	encoded, _ := json.Marshal(review.JSON200)
	if strings.Contains(string(encoded), "secret@example") {
		t.Fatalf("review response reflected private source: %s", encoded)
	}

	accepted, err := client.AcceptSkillReviewWithResponse(t.Context(), managementv1.SkillAcceptRequest{CandidateHandle: "opaque-handle"}, write)
	if err != nil || accepted.JSON200 == nil || !accepted.JSON200.Accepted || !accepted.JSON200.PublicationPending || fixture.acceptCalls != 1 {
		t.Fatalf("accept = %#v, calls = %d, %v", accepted, fixture.acceptCalls, err)
	}
	updated, err := client.ReviewSkillUpdateWithResponse(t.Context(), "calendar", managementv1.SkillObservedStateRequest{ExpectedState: "state-1"}, write)
	if err != nil || updated.JSON200 == nil || fixture.reviewUpdateCalls != 1 {
		t.Fatalf("review update = %#v, calls = %d, %v", updated, fixture.reviewUpdateCalls, err)
	}
	unconfirmed, err := client.MutateSkillWithResponse(t.Context(), "calendar", managementv1.SkillMutationRequest{Operation: managementv1.SkillLifecycleOperationRemove, ExpectedState: "state-1"}, write)
	if err != nil || unconfirmed.StatusCode() != http.StatusBadRequest || fixture.changeCalls != 0 {
		t.Fatalf("unconfirmed remove = %#v, calls = %d, %v", unconfirmed, fixture.changeCalls, err)
	}
	for _, test := range []struct {
		operation managementv1.SkillLifecycleOperation
		confirm   bool
	}{
		{operation: managementv1.SkillLifecycleOperationEnable},
		{operation: managementv1.SkillLifecycleOperationDisable, confirm: true},
		{operation: managementv1.SkillLifecycleOperationRemove, confirm: true},
	} {
		response, callErr := client.MutateSkillWithResponse(t.Context(), "calendar", managementv1.SkillMutationRequest{Operation: test.operation, ExpectedState: "state-1", Confirm: test.confirm}, write)
		if callErr != nil || response.JSON200 == nil || fixture.operation != skillcmd.Operation(test.operation) {
			t.Fatalf("%s = %#v, operation = %q, %v", test.operation, response, fixture.operation, callErr)
		}
	}
	credentials = generatedLogin(t, client)
	write = credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)
	refreshed, err := client.RefreshSkillsWithResponse(t.Context(), managementv1.SkillObservedStateRequest{ExpectedState: "state-1"}, write)
	if err != nil || refreshed.JSON200 == nil || fixture.refreshCalls != 1 {
		t.Fatalf("refresh = %#v, calls = %d, %v", refreshed, fixture.refreshCalls, err)
	}
	retried, err := client.RetrySkillPublicationWithResponse(t.Context(), write)
	if err != nil || retried.JSON200 == nil || fixture.retryCalls != 1 {
		t.Fatalf("retry = %#v, calls = %d, %v", retried, fixture.retryCalls, err)
	}
	confused, err := client.ListSkillsWithResponse(t.Context(), credentialRequestEditor("same-raw-ai-session-id", ""))
	if err != nil || confused.StatusCode() != http.StatusUnauthorized || fixture.inventoryCalls != 1 {
		t.Fatalf("AI session credential = %#v, inventory calls = %d, %v", confused, fixture.inventoryCalls, err)
	}
}

type skillFixture struct {
	authority         skillcmd.Authority
	inventoryCalls    int
	reviewCalls       int
	reviewUpdateCalls int
	acceptCalls       int
	changeCalls       int
	refreshCalls      int
	retryCalls        int
	operation         skillcmd.Operation
}

func (f *skillFixture) Inventory(_ context.Context, authority skillcmd.Authority) (skillcmd.Inventory, error) {
	f.inventoryCalls++
	f.authority = authority
	return skillcmd.Inventory{ExpectedState: "state-1", Entries: []skillcmd.Entry{skillEntryFixture()}}, nil
}

func (f *skillFixture) ReviewInstall(_ context.Context, request skillcmd.InstallReview) (skillcmd.Candidate, error) {
	f.reviewCalls++
	f.authority = request.Authority
	return skillcmd.Candidate{Handle: "opaque-handle", Entry: skillEntryFixture(), SkillPath: request.Path, ResolvedCommit: "commit-1", ExpectedState: request.ExpectedState, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *skillFixture) ReviewUpdate(_ context.Context, request skillcmd.Mutation) (skillcmd.Candidate, error) {
	f.reviewUpdateCalls++
	f.authority = request.Authority
	return skillcmd.Candidate{Handle: "opaque-handle", Entry: skillEntryFixture(), ExpectedState: request.ExpectedState, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *skillFixture) Accept(_ context.Context, _ string, authority skillcmd.Authority) (skillcmd.Outcome, error) {
	f.acceptCalls++
	f.authority = authority
	entry := skillEntryFixture()
	return skillcmd.Outcome{Accepted: true, Pending: true, Generation: 2, Entry: &entry}, errors.New("publication pending")
}

func (f *skillFixture) Change(_ context.Context, operation skillcmd.Operation, request skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.changeCalls++
	f.operation = operation
	f.authority = request.Authority
	return skillcmd.Outcome{Accepted: true}, nil
}

func (f *skillFixture) Refresh(_ context.Context, request skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.refreshCalls++
	f.authority = request.Authority
	return skillcmd.Outcome{Accepted: true}, nil
}

func (f *skillFixture) RetryPublication(_ context.Context, authority skillcmd.Authority) (skillcmd.Outcome, error) {
	f.retryCalls++
	f.authority = authority
	return skillcmd.Outcome{Accepted: true}, nil
}

func skillEntryFixture() skillcmd.Entry {
	return skillcmd.Entry{
		ID:              runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "calendar"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "calendar"},
		CurrentRevision: "rev-1", Revision: "rev-1", Name: "calendar", Description: "Calendar operations", Ownership: skillcmd.OwnershipManaged, SourceLabel: "managed", Enabled: true,
	}
}
