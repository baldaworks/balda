package sessionturnapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestFinalPublicationFailurePropagates(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "response", true: "provider error"}[terminal], func(t *testing.T) {
			runner, id := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
				done := adksession.NewEvent(t.Context(), invocationID)
				done.TurnComplete = true
				if terminal {
					done.ErrorMessage = "provider unavailable"
				} else {
					done.Content = genai.NewContentFromText("answer", genai.RoleModel)
				}
				return []*adksession.Event{done}
			})
			cause := errors.New("final queue unavailable")
			bus := &finalOutcomeDispatcher{err: cause}
			service := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
			err := service.Execute(t.Context(), ExecutionRequest{Text: "input", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "tg-origin", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: deliverycmd.Locator{SessionID: "tg-origin", ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`}})
			var accepted *turncmd.FinalOutcomeAcceptedError
			if errors.As(err, &accepted) {
				t.Fatal("failed publication marked accepted")
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Execute error=%v, want final publication error", err)
			}
		})
	}
}

type finalOutcomeDispatcher struct {
	err       error
	envelopes []actorlayer.Envelope
}

func (d *finalOutcomeDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestFinalAcceptedBeforeBookkeepingFailure(t *testing.T) {
	for _, jobBacked := range []bool{false, true} {
		for _, terminal := range []bool{false, true} {
			name := fmt.Sprintf("job-%t-terminal-%t", jobBacked, terminal)
			t.Run(name, func(t *testing.T) {
				runner, id := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
					done := adksession.NewEvent(t.Context(), invocationID)
					done.TurnComplete = true
					if terminal {
						done.ErrorMessage = "provider unavailable"
					} else {
						done.Content = genai.NewContentFromText("answer", genai.RoleModel)
					}
					return []*adksession.Event{done}
				})
				cause := errors.New("bookkeeping unavailable")
				bus := &finalOutcomeDispatcher{}
				service := NewTurnExecutionServiceWithJobEvents(bus, finalResultAppender{cause}, finalStateReader{bus: bus, err: cause}, zerolog.Nop(), automode.DefaultMaxTurns)
				req := ExecutionRequest{Text: "input", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "tg-origin", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: deliverycmd.Locator{SessionID: "tg-origin", ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`}}
				if jobBacked {
					req.JobID = "chat-correlation"
				}
				err := service.Execute(t.Context(), req)
				var accepted *turncmd.FinalOutcomeAcceptedError
				if !errors.Is(err, cause) || !errors.As(err, &accepted) || len(bus.envelopes) != 1 {
					t.Fatalf("error=%v accepted=%t deliveries=%d", err, accepted != nil, len(bus.envelopes))
				}
			})
		}
	}
}

type finalResultAppender struct{ err error }

func (a finalResultAppender) AppendEvent(context.Context, string, string, string, string, any) error {
	return a.err
}

type finalStateReader struct {
	bus *finalOutcomeDispatcher
	err error
}

func (r finalStateReader) RuntimeStateValue(context.Context, deliverycmd.Locator, string) (any, bool, error) {
	if len(r.bus.envelopes) > 0 {
		return nil, false, r.err
	}
	return nil, false, nil
}
