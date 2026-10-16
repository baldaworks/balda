package chatapp

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type archiveRecorder struct {
	input chatarchivecmd.Input
	id    string
	err   error
}

func (a *archiveRecorder) RecordInput(_ context.Context, id string, input chatarchivecmd.Input) (chatarchivecmd.Message, error) {
	a.input = input
	a.id = id
	return chatarchivecmd.Message{SessionID: id, Source: input.Source, Sequence: 1}, a.err
}

func TestArchiveAdmissionPrecedesDispatchAndSkillRewriting(t *testing.T) {
	for _, fails := range []bool{true, false} {
		t.Run(map[bool]string{true: "storage failure", false: "dispatch failure"}[fails], func(t *testing.T) {
			recorder := &archiveRecorder{}
			if fails {
				recorder.err = chatarchivecmd.ErrUnavailable
			}
			dispatcher := &recordingDispatcher{err: actorlayer.TransientError(errors.New("broker unavailable")), receipt: &actortransport.DispatchReceipt{}}
			svc, err := NewService(ServiceParams{Sessions: &stubSessionPreparer{result: SessionPreparation{Ready: true, ArchiveID: "epoch"}}, Dispatcher: dispatcher, Skills: &stubSkillPinner{}, Archive: recorder})
			if err != nil {
				t.Fatal(err)
			}
			request := testRequest("message-1", "/skill review original")
			request.OriginalInput = &chatarchivecmd.Input{Source: "message-1", Text: request.Text, ReceivedAt: request.ReceivedAt, Speaker: chatarchivecmd.Speaker{Principal: "speaker"}}
			result, err := svc.HandleChat(t.Context(), request)
			if err == nil || result.Settlement.Outcome != turncmd.InboundRetry {
				t.Fatalf("result=%+v,%v", result, err)
			}
			if recorder.input.Text != "/skill review original" || recorder.id != "epoch" {
				t.Fatalf("capture=%+v", recorder)
			}
			if fails && len(dispatcher.envelopes) != 0 {
				t.Fatal("published despite storage failure")
			}
			if !fails {
				var payload turncmd.SessionTurnPayload
				if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.ArchiveID != "epoch" || payload.Text == request.Text {
					t.Fatalf("durable payload=%+v", payload)
				}
			}
		})
	}
}
