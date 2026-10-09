//go:build integration && (sqlite || postgres)

package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
)

func checkProvider_WebhookHistory(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "webhook-history.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeContractProvider(t, p) }()
	base := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	raw := `{"token":"opaque input"}`
	admissions := []webhookcmd.Admission{
		{RouteName: "event", DedupeKey: "webhook:event:old", RequestID: "old", Prompt: "rendered old",
			JobID: "webhook-event-old", SessionID: "wh-old", CreatedAt: base},
		{RouteName: "event", DedupeKey: "webhook:event:external", RequestID: "external", Prompt: "rendered external",
			RawBody: &raw, Source: WebhookHistorySourceExternal, JobID: "webhook-event-external",
			SessionID: "wh-external", CreatedAt: base.Add(2 * time.Second)},
		{RouteName: "event", DedupeKey: "webhook-test:event:test", RequestID: "test", Prompt: "rendered test",
			RawBody: &raw, Source: WebhookHistorySourceTest, JobID: "webhook-event-test",
			SessionID: "wh-test", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, admission := range admissions {
		if _, created, err := p.WebhookAdmissions().Create(t.Context(), admission); err != nil || !created {
			t.Fatalf("create admission %s: created=%t err=%v", admission.RequestID, created, err)
		}
	}
	for _, sample := range []struct {
		name string
		body string
	}{
		{name: "binary", body: string([]byte{'a', 0, 0xff, 'b'})},
		{name: "empty", body: ""},
	} {
		admission := webhookcmd.Admission{RouteName: sample.name, DedupeKey: "webhook:" + sample.name,
			RequestID: sample.name, Prompt: "constant prompt", RawBody: &sample.body,
			JobID: "webhook-" + sample.name, SessionID: "wh-" + sample.name, CreatedAt: base}
		if _, created, err := p.WebhookAdmissions().Create(t.Context(), admission); err != nil || !created {
			t.Fatalf("create %s body: created=%t err=%v", sample.name, created, err)
		}
		got, found, err := p.WebhookAdmissions().GetHistory(t.Context(), sample.name, admission.JobID)
		if err != nil || !found || got.RawBody == nil || *got.RawBody != sample.body {
			t.Fatalf("round-trip %s body: %+v found=%t err=%v", sample.name, got, found, err)
		}
	}
	changed := admissions[1]
	changedBody := "duplicate body must not replace input"
	changed.RawBody = &changedBody
	changed.Source = WebhookHistorySourceTest
	if selected, created, err := p.WebhookAdmissions().Create(t.Context(), changed); err != nil || created ||
		selected.RawBody == nil || *selected.RawBody != raw || selected.Source != WebhookHistorySourceExternal {
		t.Fatalf("duplicate admission changed input/source: %+v created=%t err=%v", selected, created, err)
	}
	if created, err := p.Jobs().CreateJob(t.Context(), JobRecord{
		ID: admissions[2].JobID, SessionID: admissions[2].SessionID, Objective: admissions[2].Prompt,
		Status: JobStatusCompleted, PrivateRunKind: PrivateRunKindWebhook,
	}); err != nil || !created {
		t.Fatalf("create job: created=%t err=%v", created, err)
	}
	if err := p.Jobs().RecordPrivateOutput(t.Context(), admissions[2].JobID, "provider answer", false); err != nil {
		t.Fatalf("record output: %v", err)
	}
	if _, created, err := p.Jobs().ReserveDelivery(t.Context(), DeliveryRecord{
		ID: "webhook-test-delivery", DeliveryKey: admissions[2].JobID + ":delivery:final",
		JobID: admissions[2].JobID, SessionID: admissions[2].SessionID,
		Channel: "telegram", AddressKey: "123:0", Kind: "delivery", Payload: "report sent",
		PayloadHash: "report-hash",
	}); err != nil || !created {
		t.Fatalf("reserve delivery: created=%t err=%v", created, err)
	}
	if err := p.Jobs().MarkDeliverySent(t.Context(), admissions[2].JobID+":delivery:final", "provider-message"); err != nil {
		t.Fatalf("mark delivery sent: %v", err)
	}

	// Reopen without a route definition: archived routes must retain their history.
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := p.WebhookAdmissions().ListHistory(t.Context(), "event", time.Time{}, "", 2)
	if err != nil || len(history) != 2 || history[0].JobID != admissions[2].JobID ||
		history[1].JobID != admissions[1].JobID {
		t.Fatalf("first page: %+v err=%v", history, err)
	}
	if history[0].Source != WebhookHistorySourceTest || history[0].RawBody == nil || *history[0].RawBody != raw ||
		history[0].JobStatus != JobStatusCompleted || history[0].Output != "provider answer" ||
		history[0].DeliveryStatus != DeliveryStatusSent || history[0].DeliveryPayload != "report sent" {
		t.Fatalf("joined test outcome: %+v", history[0])
	}
	if history[1].Source != WebhookHistorySourceExternal || history[1].JobStatus != "admitted" ||
		history[1].DeliveryStatus != "" {
		t.Fatalf("pending external outcome: %+v", history[1])
	}
	next, err := p.WebhookAdmissions().ListHistory(t.Context(), "event", history[1].CreatedAt, history[1].JobID, 2)
	if err != nil || len(next) != 1 || next[0].JobID != admissions[0].JobID || next[0].RawBody != nil {
		t.Fatalf("older page and pre-upgrade input: %+v err=%v", next, err)
	}
	byID, found, err := p.WebhookAdmissions().GetHistory(t.Context(), "event", admissions[2].JobID)
	if err != nil || !found || byID.Output != history[0].Output {
		t.Fatalf("detail: %+v found=%t err=%v", byID, found, err)
	}
	if _, found, err := p.WebhookAdmissions().GetHistory(t.Context(), "other", admissions[2].JobID); err != nil || found {
		t.Fatalf("cross-route detail: found=%t err=%v", found, err)
	}
}
