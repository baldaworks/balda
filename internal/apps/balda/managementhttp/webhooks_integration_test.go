package managementhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
)

func TestGeneratedClientWebhookLifecycleSecretsAndDedupe(t *testing.T) {
	fixture := &webhookFixture{item: webhookroutecmd.Item{Definition: webhookroutecmd.Definition{Name: "orders", PromptTemplate: "Order: {{.Body}}", DedupeSource: webhookroutecmd.DedupeSourceRequestID},
		Source: webhookroutecmd.SourceManaged, Enabled: true, Version: 1, AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: webhookroutecmd.ManagedSecretHeader,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, secret: "one-response-secret"}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, fixture, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	inventory, err := client.ListWebhooksWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || strings.Contains(string(inventory.Body), fixture.secret) {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}
	definition := managementv1.WebhookDefinition{Name: "orders", PromptTemplate: "Updated {{.Body}}", DedupeSource: managementv1.WebhookDefinitionDedupeSourceRequestID}
	created, err := client.CreateWebhookWithResponse(t.Context(), managementv1.WebhookCreateRequest{Definition: definition}, write)
	if err != nil || created.JSON200 == nil || created.JSON200.Secret != fixture.secret || fixture.authority.UserID != browserAuthTestUsername {
		t.Fatalf("create = %#v, authority=%#v, %v", created, fixture.authority, err)
	}
	detail, err := client.GetWebhookWithResponse(t.Context(), "orders", read)
	if err != nil || detail.JSON200 == nil || strings.Contains(string(detail.Body), fixture.secret) {
		t.Fatalf("detail = %#v, %v", detail, err)
	}
	withoutCSRF, err := client.UpdateWebhookWithResponse(t.Context(), "orders", managementv1.WebhookUpdateRequest{ExpectedVersion: 1, Definition: definition}, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden {
		t.Fatalf("update without csrf = %#v, %v", withoutCSRF, err)
	}
	updated, err := client.UpdateWebhookWithResponse(t.Context(), "orders", managementv1.WebhookUpdateRequest{ExpectedVersion: 1, Definition: definition}, write)
	if err != nil || updated.JSON200 == nil {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	selected, err := client.SelectWebhookWithResponse(t.Context(), "orders", managementv1.WebhookSelectionRequest{ExpectedVersion: 2, Enabled: false}, write)
	if err != nil || selected.JSON200 == nil || selected.JSON200.Enabled {
		t.Fatalf("select = %#v, %v", selected, err)
	}
	key := uuid.New()
	testRequest := managementv1.WebhookTestRequest{ExpectedVersion: 3, RequestKey: key, ConfirmDisabled: true, Body: "binary\x00input"}
	first, err := client.TestWebhookWithResponse(t.Context(), "orders", testRequest, write)
	if err != nil || first.JSON200 == nil {
		t.Fatalf("test = %#v, %v", first, err)
	}
	second, err := client.TestWebhookWithResponse(t.Context(), "orders", testRequest, write)
	if err != nil || second.JSON200 == nil || second.JSON200.JobID != first.JSON200.JobID || fixture.testCalls != 2 {
		t.Fatalf("dedupe = %#v calls=%d %v", second, fixture.testCalls, err)
	}
	history, err := client.ListWebhookHistoryWithResponse(t.Context(), "orders", nil, read)
	if err != nil || history.JSON200 == nil || len(history.JSON200.Items) != 1 {
		t.Fatalf("history=%#v %v", history, err)
	}
	historyDetail, err := client.GetWebhookHistoryWithResponse(t.Context(), "orders", first.JSON200.JobID, read)
	if err != nil || historyDetail.JSON200 == nil || historyDetail.JSON200.Input != "binary\x00input" {
		t.Fatalf("history detail=%#v %v", historyDetail, err)
	}
	rotated, err := client.RotateWebhookSecretWithResponse(t.Context(), "orders", managementv1.WebhookVersionRequest{ExpectedVersion: 3}, write)
	if err != nil || rotated.JSON200 == nil || rotated.JSON200.Secret != fixture.secret {
		t.Fatalf("rotate=%#v %v", rotated, err)
	}
	deleted, err := client.DeleteWebhookWithResponse(t.Context(), "orders", managementv1.WebhookVersionRequest{ExpectedVersion: 4}, write)
	if err != nil || deleted.JSON200 == nil || !deleted.JSON200.Deleted {
		t.Fatalf("delete=%#v %v", deleted, err)
	}
}

type webhookFixture struct {
	item      webhookroutecmd.Item
	secret    string
	authority webhookroutecmd.Authority
	test      webhookroutecmd.TestResult
	testCalls int
	testBody  string
}

func (f *webhookFixture) Inventory(context.Context, webhookroutecmd.Authority) ([]webhookroutecmd.Item, error) {
	return []webhookroutecmd.Item{f.item}, nil
}
func (f *webhookFixture) Get(context.Context, string, webhookroutecmd.Authority) (webhookroutecmd.Item, error) {
	return f.item, nil
}
func (f *webhookFixture) Create(_ context.Context, r webhookroutecmd.Create) (webhookroutecmd.SecretResult, error) {
	f.authority = r.Authority
	f.item.Definition = r.Definition
	return webhookroutecmd.SecretResult{Item: f.item, Secret: f.secret}, nil
}
func (f *webhookFixture) Update(_ context.Context, r webhookroutecmd.Update) (webhookroutecmd.Item, error) {
	f.authority = r.Authority
	f.item.Definition = r.Definition
	f.item.Version = 2
	return f.item, nil
}
func (f *webhookFixture) SetEnabled(_ context.Context, r webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error) {
	f.authority = r.Authority
	f.item.Enabled = r.Enabled
	f.item.Version = 3
	return f.item, nil
}
func (f *webhookFixture) Delete(_ context.Context, r webhookroutecmd.Delete) (webhookroutecmd.Item, error) {
	f.authority = r.Authority
	f.item.Deleted = true
	return f.item, nil
}
func (f *webhookFixture) Rotate(_ context.Context, r webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error) {
	f.authority = r.Authority
	f.item.Version = 4
	return webhookroutecmd.SecretResult{Item: f.item, Secret: f.secret}, nil
}
func (f *webhookFixture) TestPost(_ context.Context, r webhookroutecmd.TestPost) (webhookroutecmd.TestResult, error) {
	f.authority = r.Authority
	f.testCalls++
	f.testBody = r.Body
	if f.test.JobID == "" {
		f.test = webhookroutecmd.TestResult{JobID: "job-1"}
	} else {
		f.test.Duplicate = true
	}
	return f.test, nil
}
func (f *webhookFixture) History(context.Context, string, time.Time, string, int, webhookroutecmd.Authority) ([]webhookroutecmd.HistoryItem, error) {
	return []webhookroutecmd.HistoryItem{{JobID: f.test.JobID, Source: "test", CreatedAt: time.Now().UTC(), JobStatus: "queued"}}, nil
}
func (f *webhookFixture) HistoryDetail(context.Context, string, string, webhookroutecmd.Authority) (webhookroutecmd.HistoryItem, error) {
	return webhookroutecmd.HistoryItem{JobID: f.test.JobID, Source: "test", CreatedAt: time.Now().UTC(), InputAvailable: true, Input: f.testBody, JobStatus: "queued"}, nil
}
