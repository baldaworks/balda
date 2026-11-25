package goalkeeper

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

type fakeGoalSessionAccessor struct{}

func (fakeGoalSessionAccessor) GetSession(baldasession.SessionLocator) (*baldasession.TopicSession, error) {
	return &baldasession.TopicSession{}, nil
}

func (fakeGoalSessionAccessor) RestoreSession(context.Context, baldasession.SessionContext) (*baldasession.TopicSession, error) {
	return &baldasession.TopicSession{}, nil
}

func (fakeGoalSessionAccessor) EnsureSession(context.Context, baldasession.SessionContext, string) (*baldasession.TopicSession, error) {
	return &baldasession.TopicSession{}, nil
}

func TestCoordinatorStartedDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		resume       bool
		wantDelivery int
	}{
		{name: "initial run", wantDelivery: 1},
		{name: "resumed run", resume: true, wantDelivery: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			const jobID = "goal-resume-delivery"
			payload := goalJobPayload{
				JobID: jobID,
				Locator: deliverycmd.Locator{
					ChannelType: "telegram",
					AddressKey:  "1:0",
					SessionID:   "tg-1-0",
				},
				Objective:       "ship release",
				TransportUserID: "101",
				MaxIterations:   1,
			}
			store := &fakeQuestionJobStore{}
			dispatcher := &fakeContinuationDispatcher{}
			coord := newCoordinator(ActorParams{
				JobLifecycle: store,
				JobEvents:    store,
				Dispatcher:   dispatcher,
				Logger:       zerolog.Nop(),
			})
			coord.sessionAccessor = fakeGoalSessionAccessor{}

			env := actorlayer.Envelope{ID: "goal-command", Meta: map[string]string{}}
			if test.resume {
				var err error
				env, err = goalkeepercmd.ResumeEnvelope(payload)
				if err != nil {
					t.Fatalf("ResumeEnvelope() error = %v", err)
				}
			}
			err := coord.execute(context.Background(), env, payload)
			if err == nil || !strings.Contains(err.Error(), "goal run preparer is required") {
				t.Fatalf("execute() error = %v, want missing goal run preparer", err)
			}
			if got := len(dispatcher.commands); got != test.wantDelivery {
				t.Fatalf("started deliveries = %d, want %d", got, test.wantDelivery)
			}
		})
	}
}
