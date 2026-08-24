package deliveryworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const testProviderMessageID = "message-42"

type testDispatcher struct{}

func (testDispatcher) Dispatch(context.Context, Delivery) (string, error) {
	return testProviderMessageID, nil
}

type recordingFormattedDispatcher struct {
	deliveries []Delivery
}

func (d *recordingFormattedDispatcher) Dispatch(_ context.Context, delivery Delivery) (string, error) {
	d.deliveries = append(d.deliveries, delivery)
	return testProviderMessageID, nil
}

type recordingDeliveryStore struct {
	reserveCalls int
	failedReason string
}

func (s *recordingDeliveryStore) ReserveDelivery(_ context.Context, record baldastate.DeliveryRecord) (baldastate.DeliveryRecord, bool, error) {
	s.reserveCalls++
	return record, true, nil
}

func (*recordingDeliveryStore) MarkDeliverySending(context.Context, string) error { return nil }
func (s *recordingDeliveryStore) MarkDeliveryFailed(_ context.Context, _ string, reason string) error {
	s.failedReason = reason
	return nil
}
func (*recordingDeliveryStore) MarkDeliverySent(context.Context, string, string) error { return nil }

type workflowTestFormatter struct {
	name       deliveryfmt.Name
	resultName deliveryfmt.Name
	err        error
}

func (f workflowTestFormatter) Name() deliveryfmt.Name { return f.name }

func (f workflowTestFormatter) Format(text string) (deliveryfmt.Message, error) {
	if f.err != nil {
		return deliveryfmt.Message{}, f.err
	}
	name := f.resultName
	if name == "" {
		name = f.name
	}
	return deliveryfmt.Message{Name: name, Text: "formatted:" + text, PlainFallback: text}, nil
}

func TestHandleResolvesFormattedDeliveryBeforeOutboxAndProvider(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		format        deliveryfmt.DeliveryFormat
		formatter     workflowTestFormatter
		registerRoute bool
		wantPermanent bool
		wantCalls     int
		wantText      string
	}{
		{name: "valid route", format: deliveryfmt.DeliveryFormatRichMarkdown, formatter: workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown}, registerRoute: true, wantCalls: 1, wantText: "formatted:hello"},
		{name: "unknown route", format: "unknown", formatter: workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown}, registerRoute: true, wantPermanent: true},
		{name: "formatter failure", format: deliveryfmt.DeliveryFormatRichMarkdown, formatter: workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown, err: errors.New("invalid format")}, registerRoute: true, wantPermanent: true},
		{name: "formatter name mismatch", format: deliveryfmt.DeliveryFormatRichMarkdown, formatter: workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown, resultName: deliveryfmt.NamePlainText}, registerRoute: true, wantPermanent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := workflowTestRegistry(t, test.formatter, test.registerRoute)
			dispatcher := &recordingFormattedDispatcher{}
			outbox := &recordingDeliveryStore{}
			service := NewWithRegistry(dispatcher, registry, outbox, nil, nil, nil, zerolog.Nop())
			payload := deliverycmd.Payload{
				Locator:        deliverycmd.Locator{ChannelType: deliveryfmt.TransportTelegram, AddressKey: "1:0", SessionID: "tg-1-0"},
				Mode:           deliverycmd.ModeAgentReply,
				Settlement:     deliverycmd.SettlementOutbox,
				DeliveryFormat: test.format,
				Text:           "hello",
			}
			err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload)
			if test.wantPermanent {
				if actorlayer.ClassifyError(err) != actorlayer.ErrorKindPermanent {
					t.Fatalf("Handle() error kind = %q, want permanent: %v", actorlayer.ClassifyError(err), err)
				}
			} else if err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if len(dispatcher.deliveries) != test.wantCalls {
				t.Fatalf("provider calls = %d, want %d", len(dispatcher.deliveries), test.wantCalls)
			}
			if outbox.reserveCalls != test.wantCalls {
				t.Fatalf("outbox reservations = %d, want %d", outbox.reserveCalls, test.wantCalls)
			}
			if test.wantCalls == 1 {
				message := dispatcher.deliveries[0].Message
				if message == nil || message.Name != deliveryfmt.NameTelegramRichMarkdown || message.Text != test.wantText {
					t.Fatalf("typed message = %+v, want Telegram rich Markdown %q", message, test.wantText)
				}
			}
		})
	}
}

func TestPrepareDeliveryFormatsProgressAndMediaCaptions(t *testing.T) {
	t.Parallel()

	registry := workflowTestRegistry(t, workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown}, true)
	service := NewWithRegistry(nil, registry, nil, nil, nil, nil, zerolog.Nop())
	for _, test := range []struct {
		name    string
		payload deliverycmd.Payload
	}{
		{
			name: "visible progress",
			payload: deliverycmd.Payload{Locator: deliverycmd.Locator{ChannelType: deliveryfmt.TransportTelegram}, Mode: deliverycmd.ModeProgress, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
				Progress: &deliverycmd.Progress{Visible: true, Text: "plan"}},
		},
		{
			name: "media caption",
			payload: deliverycmd.Payload{Locator: deliverycmd.Locator{ChannelType: deliveryfmt.TransportTelegram}, Mode: deliverycmd.ModePhoto, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
				Media: &deliverycmd.Media{Caption: "caption"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery, err := service.prepareDelivery(test.payload)
			if err != nil {
				t.Fatalf("prepareDelivery() error = %v", err)
			}
			if delivery.Message == nil || !strings.HasPrefix(delivery.Message.Text, "formatted:") {
				t.Fatalf("typed message = %+v, want formatted content", delivery.Message)
			}
		})
	}
}

func TestPrepareDeliveryAppliesStructuredProgressPresentation(t *testing.T) {
	t.Parallel()

	registry := workflowTestRegistry(t, workflowTestFormatter{name: deliveryfmt.NameTelegramRichMarkdown}, true)
	structured, err := progressfmt.NewStructuredRegistry()
	if err != nil {
		t.Fatalf("progressfmt.NewStructuredRegistry() error = %v", err)
	}
	service := NewWithRegistries(nil, registry, structured, nil, nil, nil, nil, zerolog.Nop())

	delivery, err := service.prepareDelivery(deliverycmd.Payload{
		Locator:        deliverycmd.Locator{ChannelType: deliveryfmt.TransportTelegram},
		Mode:           deliverycmd.ModeProgress,
		DeliveryFormat: "",
		Progress: &deliverycmd.Progress{
			Kind:    deliverycmd.ProgressPlanUpdate,
			Text:    "plan",
			Visible: true,
		},
	})
	if err != nil {
		t.Fatalf("prepareDelivery() error = %v", err)
	}
	if delivery.Payload.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q, want rich_markdown", delivery.Payload.DeliveryFormat)
	}
	if delivery.Message == nil || delivery.Message.Text != "formatted:plan" {
		t.Fatalf("typed message = %+v, want formatted progress text", delivery.Message)
	}
}

func TestHandleLogsFormatterFailureWithoutMessageOrErrorContent(t *testing.T) {
	t.Parallel()

	const (
		bodySecret      = "FORMATTER-BODY-SENTINEL"
		formatterSecret = "FORMATTER-ERROR-SENTINEL"
	)
	var logs bytes.Buffer
	registry := workflowTestRegistry(t, workflowTestFormatter{
		name: deliveryfmt.NameTelegramRichMarkdown,
		err:  errors.New(formatterSecret),
	}, true)
	service := NewWithRegistry(
		&recordingFormattedDispatcher{},
		registry,
		nil,
		nil,
		nil,
		nil,
		zerolog.New(&logs),
	)
	err := service.Handle(context.Background(), actorlayer.Envelope{ID: "formatter-safe-diagnostics"}, deliverycmd.Payload{
		Locator:        deliverycmd.Locator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "tg--1001-77"},
		Mode:           deliverycmd.ModeAgentReply,
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
		Text:           bodySecret,
	})
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindPermanent {
		t.Fatalf("Handle() error kind = %q, want permanent: %v", actorlayer.ClassifyError(err), err)
	}
	got := logs.String()
	for _, secret := range []string{bodySecret, formatterSecret} {
		if strings.Contains(got, secret) {
			t.Fatalf("formatter diagnostics contain sentinel %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"resolution_outcome":"formatter_failed"`) ||
		!strings.Contains(got, `"settlement_class":"permanent"`) {
		t.Fatalf("formatter diagnostics = %s, want formatter failure and permanent settlement", got)
	}
}

func workflowTestRegistry(t *testing.T, formatter workflowTestFormatter, registerRoute bool) *deliveryfmt.Registry {
	t.Helper()
	routes := []deliveryfmt.Route{}
	if registerRoute {
		routes = append(routes, deliveryfmt.Route{Transport: deliveryfmt.TransportTelegram, DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown, RegisteredName: deliveryfmt.NameTelegramRichMarkdown})
	}
	registry, err := deliveryfmt.NewRegistry(
		[]deliveryfmt.Format{{Name: deliveryfmt.NameTelegramRichMarkdown, Instructions: "Use rich Markdown.", Example: "**Hello**"}},
		[]deliveryfmt.FormatterRegistration{{Name: deliveryfmt.NameTelegramRichMarkdown, Formatter: formatter}},
		routes,
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

type failingDeliveryDispatcher struct {
	calls int
	err   error
}

type recordingJobEvents struct {
	payload any
}

func (e *recordingJobEvents) AppendEvent(_ context.Context, _ string, _ string, _ string, _ string, payload any) error {
	e.payload = payload
	return nil
}

func (d *failingDeliveryDispatcher) Dispatch(context.Context, Delivery) (string, error) {
	d.calls++
	if d.err != nil {
		return "", d.err
	}
	return "", errors.New("ephemeral delivery rejected")
}

type failedQuestionBinder struct {
	status  string
	failure questioncmd.Failure
}

func (*failedQuestionBinder) BindDelivery(context.Context, string, questioncmd.DeliveryRef) error {
	return nil
}

func (b *failedQuestionBinder) DeliveryState(context.Context, string) (string, bool, error) {
	status := b.status
	if status == "" {
		status = questioncmd.StatusPending
	}
	return status, true, nil
}

func (*failedQuestionBinder) FailedDeliveryContinuation(context.Context, string) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (b *failedQuestionBinder) FailDelivery(_ context.Context, _ string, failure questioncmd.Failure) (actorlayer.Envelope, bool, error) {
	b.failure = failure
	return actorlayer.Envelope{ID: "question-failed", DedupeKey: "question:question-1:failed"}, true, nil
}

type recordingActorDispatcher struct{ envelopes []actorlayer.Envelope }

func (d *recordingActorDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, envelope)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandleFailsQuestionClosedWhenProviderDeliveryFails(t *testing.T) {
	delivery := &failingDeliveryDispatcher{}
	binder := &failedQuestionBinder{}
	actor := &recordingActorDispatcher{}
	service := New(delivery, nil, nil, binder, actor, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator:  deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:     deliverycmd.ModeAgentReply,
		Refs:     map[string]string{"question_id": "question-1"},
		Question: &deliverycmd.Question{ID: "question-1", Options: []deliverycmd.QuestionOption{{ID: "deny", Label: "Deny"}}},
		Text:     "permission?",
	}
	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.calls != 1 || binder.failure.Code != "delivery_failed" {
		t.Fatalf("delivery calls = %d failure = %+v", delivery.calls, binder.failure)
	}
	if len(actor.envelopes) != 1 || actor.envelopes[0].DedupeKey != "question:question-1:failed" {
		t.Fatalf("continuations = %+v", actor.envelopes)
	}
}

func TestHandleRetriesQuestionWhenProviderDeliveryFailureIsRetryable(t *testing.T) {
	delivery := &failingDeliveryDispatcher{err: deliverycmd.RetryableError(errors.New("telegram timeout"))}
	binder := &failedQuestionBinder{}
	actor := &recordingActorDispatcher{}
	service := New(delivery, nil, nil, binder, actor, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator:  deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:     deliverycmd.ModeAgentReply,
		Refs:     map[string]string{"question_id": "question-1"},
		Question: &deliverycmd.Question{ID: "question-1", Options: []deliverycmd.QuestionOption{{ID: "deny", Label: "Deny"}}},
		Text:     "permission?",
	}

	err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindExternalDelivery {
		t.Fatalf("Handle() error kind = %q, want external delivery: %v", actorlayer.ClassifyError(err), err)
	}
	if delivery.calls != 1 {
		t.Fatalf("delivery calls = %d, want 1", delivery.calls)
	}
	if binder.failure.Code != "" {
		t.Fatalf("failure = %+v, want question left pending", binder.failure)
	}
	if len(actor.envelopes) != 0 {
		t.Fatalf("continuations = %+v, want none before retry exhaustion or timeout", actor.envelopes)
	}
}

func TestHandleRecordsSafeDeliveryFailureDiagnostics(t *testing.T) {
	t.Parallel()

	const (
		bodySecret       = "DELIVERY-BODY-SENTINEL"
		credentialSecret = "https://hooks.example.invalid/DELIVERY-CAPABILITY-SENTINEL"
		providerSecret   = "DELIVERY-PROVIDER-ERROR-SENTINEL"
	)
	var logs bytes.Buffer
	outbox := &recordingDeliveryStore{}
	events := &recordingJobEvents{}
	service := New(
		&failingDeliveryDispatcher{err: deliverycmd.PermanentError(errors.New(providerSecret))},
		outbox,
		events,
		nil,
		nil,
		zerolog.New(&logs),
	)
	payload := deliverycmd.Payload{
		Locator: deliverycmd.Locator{
			ChannelType: deliveryfmt.TransportTelegram,
			AddressKey:  "-1001:77",
			AddressJSON: credentialSecret,
			SessionID:   "tg--1001-77",
		},
		Mode:       deliverycmd.ModeAgentReply,
		Settlement: deliverycmd.SettlementOutbox,
		JobID:      "job-safe-diagnostics",
		Text:       bodySecret,
		Action:     credentialSecret,
		Refs:       map[string]string{"capability_url": credentialSecret},
	}
	err := service.Handle(context.Background(), actorlayer.Envelope{
		ID:   "delivery-safe-diagnostics",
		Meta: map[string]string{"job_id": payload.JobID},
	}, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindPermanent {
		t.Fatalf("Handle() error kind = %q, want permanent: %v", actorlayer.ClassifyError(err), err)
	}
	if outbox.failedReason != "permanent" {
		t.Fatalf("outbox failure reason = %q, want permanent", outbox.failedReason)
	}
	eventData, err := json.Marshal(events.payload)
	if err != nil {
		t.Fatalf("marshal event diagnostics: %v", err)
	}
	diagnostics := logs.String() + string(eventData) + outbox.failedReason
	for _, secret := range []string{bodySecret, credentialSecret, providerSecret} {
		if strings.Contains(diagnostics, secret) {
			t.Fatalf("delivery diagnostics contain sentinel %q: %s", secret, diagnostics)
		}
	}
	for _, field := range []string{
		`"resolution_outcome":"resolved"`,
		`"settlement_class":"permanent"`,
		`"error_class":"permanent"`,
		`"text_char_count":` + fmt.Sprint(len(bodySecret)),
	} {
		if !strings.Contains(diagnostics, field) {
			t.Fatalf("delivery diagnostics missing %s: %s", field, diagnostics)
		}
	}
}

func TestHandleSuppressesLateQuestionDeliveryAfterSettlement(t *testing.T) {
	delivery := &failingDeliveryDispatcher{}
	binder := &failedQuestionBinder{status: questioncmd.StatusTimedOut}
	service := New(delivery, nil, nil, binder, nil, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:    deliverycmd.ModeAgentReply,
		Refs:    map[string]string{"question_id": "question-1"},
		Text:    "permission?",
	}

	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.calls != 0 {
		t.Fatalf("delivery calls = %d, want 0 after question settlement", delivery.calls)
	}
}

type testQuestionBinder struct {
	questionID string
	ref        questioncmd.DeliveryRef
}

func (*testQuestionBinder) DeliveryState(context.Context, string) (string, bool, error) {
	return questioncmd.StatusPending, true, nil
}

func (*testQuestionBinder) FailedDeliveryContinuation(context.Context, string) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (*testQuestionBinder) FailDelivery(context.Context, string, questioncmd.Failure) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (b *testQuestionBinder) BindDelivery(_ context.Context, questionID string, ref questioncmd.DeliveryRef) error {
	b.questionID = questionID
	b.ref = ref
	return nil
}

func TestHandleBindsQuestionToProviderMessage(t *testing.T) {
	binder := &testQuestionBinder{}
	service := New(testDispatcher{}, nil, nil, binder, nil, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator: deliverycmd.Locator{
			ChannelType: "telegram",
			AddressKey:  "1:0",
			AddressJSON: `{"chat_id":1,"topic_id":0}`,
			SessionID:   "tg-1-0",
		},
		Mode:       deliverycmd.ModeAgentReply,
		Settlement: deliverycmd.SettlementOutbox,
		Refs:       map[string]string{"question_id": "question-1"},
		Text:       "permission?",
	}
	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if binder.questionID != "question-1" || binder.ref.ProviderMessageID != testProviderMessageID || binder.ref.Provider != "telegram" {
		t.Fatalf("binding = %q %+v", binder.questionID, binder.ref)
	}
}
