package actors

import (
	"context"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
)

type testPermissionSink struct {
	reviewID string
	optionID string
}

func (s *testPermissionSink) Resolve(reviewID, optionID string) {
	s.reviewID = reviewID
	s.optionID = optionID
}

func TestPermissionActorResolvesSelectedOption(t *testing.T) {
	payload, err := actorlayer.MarshalPayload(questioncmd.AnsweredContinuation{
		Resume: questioncmd.ResumeTarget{To: "permission:review-1"},
		Answer: questioncmd.Answer{SelectedOption: "allow-once"},
	})
	if err != nil {
		t.Fatalf("MarshalPayload() error = %v", err)
	}
	sink := &testPermissionSink{}
	actor := &permissionActor{sink: sink}
	err = actor.Handle(context.Background(), actorlayer.Envelope{
		Namespace: actorcmd.NamespacePermissionCommand,
		Kind:      actorcmd.KindQuestionAnswered,
		To:        actorlayer.ActorAddress{Target: actorcmd.ActorTypePermission, Key: "review-1"},
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sink.reviewID != "review-1" || sink.optionID != "allow-once" {
		t.Fatalf("resolution = %q %q", sink.reviewID, sink.optionID)
	}
}

func TestPermissionActorResolvesTimeoutAsCancellation(t *testing.T) {
	payload, err := actorlayer.MarshalPayload(questioncmd.TimedOutContinuation{
		Resume: questioncmd.ResumeTarget{To: "permission:review-1"},
	})
	if err != nil {
		t.Fatalf("MarshalPayload() error = %v", err)
	}
	sink := &testPermissionSink{}
	actor := &permissionActor{sink: sink}
	err = actor.Handle(context.Background(), actorlayer.Envelope{
		Namespace: actorcmd.NamespacePermissionCommand,
		Kind:      actorcmd.KindQuestionTimedOut,
		To:        actorlayer.ActorAddress{Target: actorcmd.ActorTypePermission, Key: "review-1"},
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sink.reviewID != "review-1" || sink.optionID != "" {
		t.Fatalf("resolution = %q %q", sink.reviewID, sink.optionID)
	}
}
