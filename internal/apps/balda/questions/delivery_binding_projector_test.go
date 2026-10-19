package questions

import (
	"context"
	"testing"
	"time"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

func TestDeliveryBindingProjectorBindsQuestionRefFromDeliveryEvent(t *testing.T) {
	store := &fakeStore{}
	projector := NewDeliveryBindingProjector(deliveryBindingProjectorParams{
		Service: New(store, nil, zerolog.Nop()),
		Logger:  zerolog.Nop(),
	})
	if err := projector.Project(context.Background(), baldaexecution.SubjectEventDeliverySent, actorlayer.Envelope{
		Payload: actorlayer.Payload{
			Encoding: actorlayer.EncodingJSON,
			Data: []byte(`{
				"provider":"telegram",
				"conversation_key":"1:0",
				"provider_message_id":"42",
 "provider_created_at":"2026-01-01T12:34:56.789Z",
				"control_handle":"telegram:message:delete",
				"refs":{"question_id":"question-1"}
			}`),
		},
	}); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if store.record.QuestionID != "question-1" {
		t.Fatalf("question id = %q, want question-1", store.record.QuestionID)
	}
	if store.record.ProviderMessageID != "42" {
		t.Fatalf("provider message id = %q, want 42", store.record.ProviderMessageID)
	}
	want := time.Date(2026, 1, 1, 12, 34, 56, 789000000, time.UTC)
	if !store.record.ProviderCreatedAt.Equal(want) {
		t.Fatalf("provider publication = %v, want %v", store.record.ProviderCreatedAt, want)
	}
	if store.record.ControlHandle != "telegram:message:delete" {
		t.Fatalf("control handle = %q, want telegram:message:delete", store.record.ControlHandle)
	}
}
