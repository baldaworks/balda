package actors

import (
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
)

func TestUnwrittenFinalStopsModelRetryWithoutFailureDelivery(t *testing.T) {
	err := &turncmd.FinalArchiveError{Cause: chatarchivecmd.ErrUnavailable}
	env := actorlayer.Envelope{Namespace: actorcmd.NamespaceHumanInbound}
	payload := turncmd.SessionTurnPayload{Deliver: true, Source: turncmd.SourceTelegram}
	executor := NewSessionActor(SessionActorConfig{})
	if got := executor.reportTerminalFailure(t.Context(), env, payload, err); got != nil {
		t.Fatalf("unexpected failure delivery: %v", got)
	}
	got := newSessionSettlementCoordinator(nil, nil).settle(t.Context(), env, payload, err)
	if !errors.Is(got, chatarchivecmd.ErrUnavailable) || actorlayer.IsRetryableError(got) {
		t.Fatalf("settlement=%v", got)
	}
}

func TestSteeringBatchRetainsConstituentAttachments(t *testing.T) {
	first := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "a", MessageID: 1, ReplyToMessageID: 10, RequesterUserID: "human", Text: "A", Attachments: []attachment.Descriptor{{FileName: "a.txt"}}}
	next := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "b", MessageID: 2, ReplyToMessageID: 10, RequesterUserID: "human", Text: "B", Attachments: []attachment.Descriptor{{FileName: "b.txt"}}}
	batch := &queuedTurn{task: TurnTask{SessionTurn: initializeSteeringBatch(&first)}, ctx: t.Context()}
	incoming := &queuedTurn{task: TurnTask{SessionTurn: &next}, ctx: t.Context()}
	if !mergeIntoSteeringBatch(batch, incoming) {
		t.Fatal("batch not merged")
	}
	payload := batch.task.SessionTurn
	if len(payload.Attachments) != 2 || len(payload.SteeringMessages) != 2 || payload.SteeringMessages[1].Attachments[0].FileName != "b.txt" {
		t.Fatalf("batch=%+v", payload)
	}
}
