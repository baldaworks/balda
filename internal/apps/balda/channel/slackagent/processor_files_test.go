package slackagent

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type currentFileIngestorStub struct {
	files       []FileRef
	attachments []attachment.Descriptor
	err         error
}

func (s *currentFileIngestorStub) Ingest(_ context.Context, files []FileRef) ([]attachment.Descriptor, error) {
	s.files = append([]FileRef(nil), files...)
	return attachment.NormalizeList(s.attachments), s.err
}

type chatHandlerRecorder struct {
	requests []chatapp.Request
	result   chatapp.Result
	err      error
}

func (h *chatHandlerRecorder) HandleChat(_ context.Context, request chatapp.Request) (chatapp.Result, error) {
	h.requests = append(h.requests, request)
	return h.result, h.err
}

type processorLifecycleStub struct{}

func (processorLifecycleStub) BeginTurn(context.Context, deliverycmd.Locator, string, string) error {
	return nil
}
func (processorLifecycleStub) HandleSessionStopped(context.Context, deliverycmd.Locator) error {
	return nil
}
func (processorLifecycleStub) CloseSession(context.Context, deliverycmd.Locator) error { return nil }

func TestInboundProcessorAddsCompletePersistedFileSetBeforeChat(t *testing.T) {
	t.Parallel()
	descriptors := []attachment.Descriptor{
		{Kind: attachment.KindPhoto, FileID: "F1", Blob: &attachment.BlobRef{Store: "local", Path: "/state/F1"}},
		{Kind: attachment.KindDocument, FileID: "F2", Blob: &attachment.BlobRef{Store: "local", Path: "/state/F2"}},
	}
	files := &currentFileIngestorStub{attachments: descriptors}
	chat := &chatHandlerRecorder{result: chatapp.Result{Settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}}}
	processor := NewInboundProcessor(chat, processorLifecycleStub{}, nil, files)
	envelope := IngressEnvelope{
		Files: []FileRef{{ID: "F1"}, {ID: "F2"}},
		Chat:  chatapp.Request{Text: "", Locator: deliverycmd.Locator{SessionID: "session"}},
	}

	settlement, err := processor.ProcessInbound(context.Background(), envelope)
	if err != nil || settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("ProcessInbound() = %+v, %v", settlement, err)
	}
	if len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 2 {
		t.Fatalf("chat requests = %+v, want one request with two attachments", chat.requests)
	}
	if chat.requests[0].Attachments[0].FileID != "F1" || chat.requests[0].Attachments[1].FileID != "F2" {
		t.Fatalf("chat attachment order = %+v", chat.requests[0].Attachments)
	}
}

func TestInboundProcessorSettlesFileFailuresBeforeChat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		err         error
		wantOutcome turncmd.InboundOutcome
		wantReason  string
	}{
		{name: "terminal limit", err: newFileError("F1", "file_size_exceeded", false, attachment.ErrTooLarge), wantOutcome: turncmd.InboundTerminal, wantReason: "file_size_exceeded"},
		{name: "retryable download", err: &APIError{Method: "files.download", StatusCode: 429, Retryable: true}, wantOutcome: turncmd.InboundRetry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := &currentFileIngestorStub{err: test.err}
			chat := &chatHandlerRecorder{}
			processor := NewInboundProcessor(chat, processorLifecycleStub{}, nil, files)

			settlement, err := processor.ProcessInbound(context.Background(), IngressEnvelope{
				Files: []FileRef{{ID: "F1"}},
				Chat:  chatapp.Request{Locator: deliverycmd.Locator{SessionID: "session"}},
			})
			if err == nil {
				t.Fatalf("ProcessInbound() error = %v, want wrapped failure", err)
			}
			if settlement.Outcome != test.wantOutcome || settlement.Reason != test.wantReason {
				t.Fatalf("settlement = %+v, want outcome=%s reason=%q", settlement, test.wantOutcome, test.wantReason)
			}
			if len(chat.requests) != 0 {
				t.Fatalf("chat requests = %d, want 0", len(chat.requests))
			}
		})
	}
}
