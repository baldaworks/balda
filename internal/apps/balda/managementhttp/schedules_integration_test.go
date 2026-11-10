package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
)

func TestGeneratedClientScheduleLifecycleAndRedaction(t *testing.T) {
	fixture := &scheduleFixture{item: schedulecmd.Item{Definition: schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Locator: "telegram:1", Content: "secret schedule content"},
		Source: "managed", Enabled: true, Version: 1, Status: "ready"}}
	server := httptest.NewServer(newProviderTestHandlerWithMCP(t, nil, fixture))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	inventory, err := client.ListSchedulesWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Items) != 1 {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	if string(inventory.Body) == "" || containsBytes(inventory.Body, []byte(fixture.item.Definition.Content)) {
		t.Fatalf("inventory disclosed guarded content: %s", inventory.Body)
	}
	detail, err := client.GetScheduleWithResponse(t.Context(), "daily", read)
	if err != nil || detail.JSON200 == nil || detail.JSON200.Definition.Content != fixture.item.Definition.Content {
		t.Fatalf("detail = %#v, %v", detail, err)
	}

	definition := managementv1.ScheduleDefinition{ID: "daily", Cron: "30 9 * * *", Locator: "telegram:2", Content: "updated"}
	created, err := client.CreateScheduleWithResponse(t.Context(), managementv1.ScheduleCreateRequest{Definition: definition}, write)
	if err != nil || created.JSON200 == nil || fixture.lastAuthority.WebAuthSessionID == "" || fixture.lastAuthority.UserID != browserAuthTestUsername {
		t.Fatalf("create = %#v, authority = %#v, err = %v", created, fixture.lastAuthority, err)
	}
	withoutCSRF, err := client.UpdateScheduleWithResponse(t.Context(), "daily", managementv1.ScheduleUpdateRequest{ExpectedVersion: 1, Definition: definition}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden {
		t.Fatalf("update without CSRF = %#v, %v", withoutCSRF, err)
	}
	updated, err := client.UpdateScheduleWithResponse(t.Context(), "daily", managementv1.ScheduleUpdateRequest{ExpectedVersion: 1, Definition: definition}, write)
	if err != nil || updated.JSON200 == nil {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	selected, err := client.SelectScheduleWithResponse(t.Context(), "daily", managementv1.ScheduleSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
	if err != nil || selected.JSON200 == nil || selected.JSON200.Enabled {
		t.Fatalf("selection = %#v, %v", selected, err)
	}

	requestKey := uuid.NewString()
	first, err := client.RunScheduleNowWithResponse(t.Context(), "daily", managementv1.ScheduleRunNowRequest{RequestKey: requestKey, ConfirmDisabled: true}, write)
	if err != nil || first.JSON200 == nil {
		t.Fatalf("run = %#v, %v", first, err)
	}
	second, err := client.RunScheduleNowWithResponse(t.Context(), "daily", managementv1.ScheduleRunNowRequest{RequestKey: requestKey, ConfirmDisabled: true}, write)
	if err != nil || second.JSON200 == nil || second.JSON200.ID != first.JSON200.ID || fixture.runCalls != 2 {
		t.Fatalf("deduplicated run = %#v, calls = %d, err = %v", second, fixture.runCalls, err)
	}
	history, err := client.ListScheduleRunsWithResponse(t.Context(), "daily", nil, read)
	if err != nil || history.JSON200 == nil || len(history.JSON200.Items) != 1 {
		t.Fatalf("history = %#v, %v", history, err)
	}
	runDetail, err := client.GetScheduleRunWithResponse(t.Context(), "daily", first.JSON200.ID, read)
	if err != nil || runDetail.JSON200 == nil || runDetail.JSON200.Run.ReportLocatorRef != "telegram:2" || runDetail.JSON200.Input != "frozen input" {
		t.Fatalf("run detail = %#v, %v", runDetail, err)
	}
	deleted, err := client.DeleteScheduleWithResponse(t.Context(), "daily", managementv1.ScheduleDeleteRequest{ExpectedVersion: 3}, write)
	if err != nil || deleted.JSON200 == nil || !deleted.JSON200.Deleted {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
}

func containsBytes(data, part []byte) bool {
	for len(data) >= len(part) {
		if string(data[:len(part)]) == string(part) {
			return true
		}
		data = data[1:]
	}
	return false
}

type scheduleFixture struct {
	item          schedulecmd.Item
	run           schedulecmd.RunItem
	lastAuthority schedulecmd.Authority
	runCalls      int
}

func (f *scheduleFixture) Inventory(context.Context, schedulecmd.Authority) ([]schedulecmd.Item, error) {
	return []schedulecmd.Item{f.item}, nil
}
func (f *scheduleFixture) Get(_ context.Context, _ string, authority schedulecmd.Authority) (schedulecmd.Item, error) {
	f.lastAuthority = authority
	return f.item, nil
}
func (f *scheduleFixture) Create(_ context.Context, request schedulecmd.Create) (schedulecmd.Item, error) {
	f.lastAuthority, f.item.Definition = request.Authority, request.Definition
	return f.item, nil
}
func (f *scheduleFixture) Update(_ context.Context, request schedulecmd.Update) (schedulecmd.Item, error) {
	f.lastAuthority, f.item.Definition, f.item.Version = request.Authority, request.Definition, 2
	return f.item, nil
}
func (f *scheduleFixture) SetEnabled(_ context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error) {
	f.lastAuthority, f.item.Enabled, f.item.Version = request.Authority, request.Enabled, 3
	return f.item, nil
}
func (f *scheduleFixture) Delete(_ context.Context, request schedulecmd.Delete) (schedulecmd.Item, error) {
	f.lastAuthority, f.item.Deleted = request.Authority, true
	return f.item, nil
}
func (f *scheduleFixture) RunNow(_ context.Context, request schedulecmd.RunNow) (schedulecmd.RunItem, error) {
	f.lastAuthority, f.runCalls = request.Authority, f.runCalls+1
	if f.run.ID == "" {
		f.run = schedulecmd.RunItem{ID: uuid.NewString(), Trigger: "manual", RequestedAt: time.Now().UTC(), State: "queued", ReportLocatorRef: f.item.Definition.Locator}
	}
	return f.run, nil
}
func (f *scheduleFixture) History(context.Context, string, time.Time, string, int, schedulecmd.Authority) ([]schedulecmd.RunItem, error) {
	return []schedulecmd.RunItem{f.run}, nil
}
func (f *scheduleFixture) RunDetail(context.Context, string, string, schedulecmd.Authority) (schedulecmd.RunDetail, error) {
	return schedulecmd.RunDetail{Run: f.run, Input: "frozen input", Output: "durable output"}, nil
}
