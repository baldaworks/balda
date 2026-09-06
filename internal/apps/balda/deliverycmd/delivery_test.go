package deliverycmd

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/go-actorlayer"
)

func TestQuestionEnvelopeCarriesTransportNeutralOptions(t *testing.T) {
	env, err := QuestionEnvelope(
		"",
		actorlayer.SystemAddress("test"),
		testLocator(),
		deliveryfmt.DeliveryFormatRichMarkdown,
		SettlementOutbox,
		"Choose",
		"question-1",
		"test",
		[]QuestionOption{{ID: "allow", Label: "Allow"}, {ID: "cancel", Label: "Cancel"}},
		QuestionAudience{Visibility: QuestionVisibilityPrivate, UserID: "user-1"},
	)
	if err != nil {
		t.Fatalf("QuestionEnvelope() error = %v", err)
	}
	var payload Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Question == nil || payload.Question.ID != "question-1" || len(payload.Question.Options) != 2 {
		t.Fatalf("question = %+v", payload.Question)
	}
	if payload.Question.Audience.Visibility != QuestionVisibilityPrivate || payload.Question.Audience.UserID != "user-1" {
		t.Fatalf("question audience = %+v", payload.Question.Audience)
	}
	if payload.Refs["question_id"] != "question-1" {
		t.Fatalf("refs = %+v", payload.Refs)
	}
}

func TestAgentReplyEnvelopeCarriesOnlyDeliveryFormat(t *testing.T) {
	t.Parallel()

	env, err := AgentReplyEnvelopeWithFormat(
		"",
		actorlayer.SystemAddress("test"),
		testLocator(),
		deliveryfmt.DeliveryFormatRichHTML,
		"hello",
		"test",
	)
	if err != nil {
		t.Fatalf("AgentReplyEnvelopeWithFormat() error = %v", err)
	}

	var payload Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.DeliveryFormat != deliveryfmt.DeliveryFormatRichHTML {
		t.Errorf("DeliveryFormat = %q, want %q", payload.DeliveryFormat, deliveryfmt.DeliveryFormatRichHTML)
	}
}

func TestMarkdownEnvelopeWithFormatAndSettlementCarriesSystemPresentation(t *testing.T) {
	t.Parallel()

	locator := testLocator()
	env, err := MarkdownEnvelopeWithFormatAndSettlement(
		"",
		actorlayer.SystemAddress("command"),
		locator,
		deliveryfmt.DeliveryFormatMrkdwn,
		SettlementBypass,
		"*Balda locator*",
		"locator",
	)
	if err != nil {
		t.Fatalf("MarkdownEnvelopeWithFormatAndSettlement() error = %v", err)
	}
	var payload Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Mode != ModeMarkdown || payload.DeliveryFormat != deliveryfmt.DeliveryFormatMrkdwn || payload.Settlement != SettlementBypass {
		t.Fatalf("payload delivery contract = %+v", payload)
	}
	if payload.Locator != locator || payload.Text != "*Balda locator*" {
		t.Fatalf("payload content = %+v", payload)
	}
}

func TestClearQuestionControlsEnvelopeIsIdempotentlyKeyed(t *testing.T) {
	env, err := SettleQuestionControlsEnvelope(actorlayer.SystemAddress("question"), testLocator(), "question-1", "42", "telegram:inline:clear", "Allow")
	if err != nil {
		t.Fatalf("ClearQuestionControlsEnvelope() error = %v", err)
	}
	if env.DedupeKey != "question:question-1:controls:clear" {
		t.Fatalf("dedupe key = %q", env.DedupeKey)
	}
	var payload Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Mode != ModeClearQuestionControls || payload.MessageID != "42" || payload.Handle != "telegram:inline:clear" || payload.Text != "Allow" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestValidateQuestionRejectsDuplicateOptions(t *testing.T) {
	err := Validate(Payload{
		Mode: ModeAgentReply,
		Text: "Choose",
		Question: &Question{ID: "question-1", Options: []QuestionOption{
			{ID: "same", Label: "First"},
			{ID: "same", Label: "Second"},
		}},
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want duplicate option error")
	}
}

func testLocator() Locator {
	return Locator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`}
}
