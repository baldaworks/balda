package slackagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	baldachannel "github.com/baldaworks/balda/internal/apps/balda/channel"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

func TestSlackMediaWorkflowStoresProviderFileID(t *testing.T) {
	ctx := t.Context()
	fixture := newSlackUploadFixture(t, func(stage string, attempt int) int {
		return http.StatusOK
	})
	provider := newWorkflowSQLiteProvider(t, ctx)
	service := newSlackMediaWorkflow(provider, fixture.client)
	env, payload := newDocumentDelivery(t, fixture.path)

	if err := service.Handle(ctx, env, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	record := inspectDelivery(t, ctx, provider.Jobs(), env, payload)
	if record.Status != baldastate.DeliveryStatusSent || record.ProviderMessageID != testOutboundFileID {
		t.Fatalf("delivery record = %+v, want sent Slack file ID", record)
	}
	if fixture.ticketCalls != 1 || fixture.byteCalls != 1 || fixture.completionCalls != 1 {
		t.Fatalf("upload calls = ticket:%d bytes:%d completion:%d", fixture.ticketCalls, fixture.byteCalls, fixture.completionCalls)
	}
}

func TestSlackMediaWorkflowSuppressesResendAfterAmbiguousCompletion(t *testing.T) {
	ctx := t.Context()
	fixture := newSlackUploadFixture(t, func(stage string, attempt int) int {
		if stage == "completion" {
			return http.StatusBadGateway
		}
		return http.StatusOK
	})
	dbPath := filepath.Join(t.TempDir(), "delivery.db")
	provider1, err := baldastate.NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	service1 := newSlackMediaWorkflow(provider1, fixture.client)
	env, payload := newDocumentDelivery(t, fixture.path)

	err = service1.Handle(ctx, env, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindExternalDelivery {
		t.Fatalf("Handle() error kind = %v, want external delivery (err: %v)", actorlayer.ClassifyError(err), err)
	}
	record := inspectDelivery(t, ctx, provider1.Jobs(), env, payload)
	if record.Status != baldastate.DeliveryStatusSending {
		t.Fatalf("delivery status = %q, want sending", record.Status)
	}
	if err := provider1.Close(); err != nil {
		t.Fatalf("provider1.Close() error = %v", err)
	}

	provider2, err := baldastate.NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider2.Close() })
	service2 := newSlackMediaWorkflow(provider2, fixture.client)
	err = service2.Handle(ctx, env, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindTransient || !strings.Contains(err.Error(), "automatic resend is disabled") {
		t.Fatalf("replayed Handle() error = %v, want transient resend suppression", err)
	}
	if fixture.ticketCalls != 1 || fixture.byteCalls != 1 || fixture.completionCalls != 1 {
		t.Fatalf("replayed upload calls = ticket:%d bytes:%d completion:%d, want one attempt", fixture.ticketCalls, fixture.byteCalls, fixture.completionCalls)
	}
}

func TestSlackMediaWorkflowRetriesSafePreCompletionFailure(t *testing.T) {
	ctx := t.Context()
	fixture := newSlackUploadFixture(t, func(stage string, attempt int) int {
		if stage == "ticket" && attempt == 1 {
			return http.StatusBadGateway
		}
		return http.StatusOK
	})
	provider := newWorkflowSQLiteProvider(t, ctx)
	service := newSlackMediaWorkflow(provider, fixture.client)
	env, payload := newDocumentDelivery(t, fixture.path)

	err := service.Handle(ctx, env, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindExternalDelivery {
		t.Fatalf("first Handle() error kind = %v, want external delivery (err: %v)", actorlayer.ClassifyError(err), err)
	}
	if record := inspectDelivery(t, ctx, provider.Jobs(), env, payload); record.Status != baldastate.DeliveryStatusFailed {
		t.Fatalf("delivery status after retryable failure = %q, want failed", record.Status)
	}
	if err := service.Handle(ctx, env, payload); err != nil {
		t.Fatalf("retry Handle() error = %v", err)
	}
	record := inspectDelivery(t, ctx, provider.Jobs(), env, payload)
	if record.Status != baldastate.DeliveryStatusSent || record.ProviderMessageID != testOutboundFileID {
		t.Fatalf("delivery record after retry = %+v, want sent", record)
	}
	if fixture.ticketCalls != 2 || fixture.byteCalls != 1 || fixture.completionCalls != 1 {
		t.Fatalf("retry upload calls = ticket:%d bytes:%d completion:%d", fixture.ticketCalls, fixture.byteCalls, fixture.completionCalls)
	}
}

type slackUploadFixture struct {
	client          *Client
	path            string
	ticketCalls     int
	byteCalls       int
	completionCalls int
}

func newSlackUploadFixture(t *testing.T, status func(stage string, attempt int) int) *slackUploadFixture {
	t.Helper()
	fixture := &slackUploadFixture{path: filepath.Join(t.TempDir(), "report.txt")}
	if err := writeTestFile(fixture.path, "report body"); err != nil {
		t.Fatalf("write media fixture: %v", err)
	}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			fixture.ticketCalls++
			if code := status("ticket", fixture.ticketCalls); code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+testUploadPath+`","file_id":"`+testOutboundFileID+`"}`)
		case testUploadPath:
			fixture.byteCalls++
			if code := status("bytes", fixture.byteCalls); code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, "OK")
		case testCompleteUploadPath:
			fixture.completionCalls++
			if code := status("completion", fixture.completionCalls); code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"`+testOutboundFileID+`"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	fixture.client = NewClientWithBaseURL(server.URL, "xoxb-secret")
	fixture.client.http = server.Client()
	fixture.client.validateFileURL = exactTestServerValidator(t, server.URL)
	return fixture
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func newWorkflowSQLiteProvider(t *testing.T, ctx context.Context) baldastate.Provider {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "delivery.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func newSlackMediaWorkflow(provider baldastate.Provider, client *Client) *deliveryworkflow.Service {
	adapter := NewAdapter(nil, client, attachment.Limits{MaxFileBytes: 1024}, zerolog.Nop(), AdapterConfig{})
	router := baldachannel.NewRouter(map[string]deliverycmd.Adapter{ChannelType: adapter})
	return deliveryworkflow.New(deliveryfx.NewChannelDispatcher(router), provider.Jobs(), nil, nil, nil, zerolog.Nop())
}

func newDocumentDelivery(t *testing.T, path string) (actorlayer.Envelope, deliverycmd.Payload) {
	t.Helper()
	env, err := deliverycmd.DocumentLocalEnvelope("", actorlayer.ActorAddress{}, NewThreadLocator("T123", "C456", "171.25"), deliverycmd.SettlementOutbox, path, "", "caption", "report.txt", "text/plain", "workflow-test")
	if err != nil {
		t.Fatalf("DocumentLocalEnvelope() error = %v", err)
	}
	var payload deliverycmd.Payload
	if err := json.Unmarshal(env.Payload.Data, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return env, payload
}

func inspectDelivery(t *testing.T, ctx context.Context, store baldastate.DeliveryStore, env actorlayer.Envelope, payload deliverycmd.Payload) baldastate.DeliveryRecord {
	t.Helper()
	sum := sha256.Sum256(env.Payload.Data)
	record, created, err := store.ReserveDelivery(ctx, baldastate.DeliveryRecord{
		ID:          "inspection-record",
		DeliveryKey: env.DedupeKey,
		SessionID:   payload.Locator.SessionID,
		Channel:     payload.Locator.ChannelType,
		AddressKey:  payload.Locator.AddressKey,
		Kind:        env.Kind,
		Payload:     env.Payload.String(),
		PayloadHash: hex.EncodeToString(sum[:]),
		Status:      baldastate.DeliveryStatusPending,
	})
	if err != nil {
		t.Fatalf("ReserveDelivery(inspect) error = %v", err)
	}
	if created {
		t.Fatal("ReserveDelivery(inspect) created a record, want existing")
	}
	return record
}
