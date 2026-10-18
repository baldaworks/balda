package ingressapp

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type inputArchive struct {
	err   error
	epoch string
	input chatarchivecmd.Input
}

func (a *inputArchive) RecordInput(_ context.Context, id string, input chatarchivecmd.Input) (chatarchivecmd.Message, error) {
	a.input = input
	if a.epoch == "" {
		a.epoch = id
	}
	return chatarchivecmd.Message{SessionID: a.epoch}, a.err
}

func TestInputArchiveFailureAndStaleRetryPreventDispatch(t *testing.T) {
	for _, test := range []struct {
		name    string
		archive *inputArchive
		outcome turncmd.InboundOutcome
	}{{"unavailable", &inputArchive{err: chatarchivecmd.ErrUnavailable}, turncmd.InboundRetry}, {"previous lifetime", &inputArchive{epoch: "old"}, turncmd.InboundTerminal}} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &recordingDispatcher{receipt: &actortransport.DispatchReceipt{}}
			svc, err := New(&stubAuthorizer{result: Authorization{Allowed: true}}, &stubSessionPreparer{result: SessionPreparation{Ready: true, ArchiveID: "new"}}, dispatcher)
			if err != nil {
				t.Fatal(err)
			}
			svc.SetArchiveRecorder(test.archive)
			inbound := validInbound("message")
			inbound.OriginalInput = &chatarchivecmd.Input{Source: "message", Text: "original", ReceivedAt: time.Now()}
			result, _ := svc.Process(t.Context(), inbound)
			if result.Settlement.Outcome != test.outcome || len(dispatcher.envelopes) != 0 {
				t.Fatalf("dispatch occurred: %+v,%+v", result, dispatcher.envelopes)
			}
		})
	}
}
