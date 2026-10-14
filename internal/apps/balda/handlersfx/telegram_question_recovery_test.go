package handlersfx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type callbackAdmissionFailure struct {
	commandoutbox.OutboxStore
	commit bool
}

func (s callbackAdmissionFailure) Set(ctx context.Context, key, value string) error {
	if s.commit {
		if err := s.OutboxStore.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return errors.New("interrupted local admission")
}

type callbackRecoveryBus struct {
	offline   bool
	envelopes []actorlayer.Envelope
}

func (b *callbackRecoveryBus) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if b.offline {
		return nil, errors.New("NATS unavailable")
	}
	b.envelopes = append(b.envelopes, env)
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

type callbackAckFailure struct{ *fakeTestChannel }

func (c callbackAckFailure) AnswerQuestionCallback(ctx context.Context, id, text string, alert bool) error {
	_ = c.fakeTestChannel.AnswerQuestionCallback(ctx, id, text, alert)
	return errors.New("Telegram unavailable")
}

func TestTelegramCallbackContinuationSurvivesFailureAndRestart(t *testing.T) {
	for _, failure := range []string{"dispatch_error", "uncertain_write", "crash_before_dispatch", "bus_outage", "ack_error", "missing_dispatcher"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "state.db")
			provider, err := state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if provider != nil {
					_ = provider.Close()
				}
			})
			h := newTestHarness(t)
			h.handler.collaboratorStore, _ = newTestCollaboratorStore(auth.TelegramSubject(101))
			record := testTelegramQuestionRecord(t, 101)
			locator := baldatelegram.NewLocator(9001, 0)
			record.ChannelKind, record.AddressKey, record.AddressJSON = "telegram", locator.AddressKey, locator.AddressJSON
			record.Prompt = "Allow?"
			if err := provider.Questions().CreatePendingQuestion(ctx, record); err != nil {
				t.Fatal(err)
			}
			h.handler.questionService = questions.New(provider.Questions(), nil, zerolog.Nop())
			var store commandoutbox.OutboxStore = provider.AppKV()
			if failure == "dispatch_error" || failure == "uncertain_write" {
				store = callbackAdmissionFailure{OutboxStore: store, commit: failure == "uncertain_write"}
			}
			bus := &callbackRecoveryBus{offline: true}
			d, err := commandoutbox.NewDispatcher(store, bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			h.handler.questionDispatcher = d
			if failure == "missing_dispatcher" {
				h.handler.questionDispatcher = nil
			}
			if failure == "ack_error" {
				h.handler.channel = callbackAckFailure{h.channel}
			}
			callback := baldatelegram.CallbackContext{Locator: locator, CallbackQueryID: "callback-1", QuestionID: record.QuestionID, ProviderMessageID: "42", UserID: 101, OptionIndex: 1}
			selection := questioncmd.InboundSelection{Provider: "telegram", SessionID: locator.SessionID, ConversationKey: locator.AddressKey, QuestionID: record.QuestionID, ProviderMessageID: "42", User: questioncmd.UserRef{UserID: baldatelegram.UserID(101)}, OptionIndex: 1}
			if failure == "crash_before_dispatch" {
				result, err := h.handler.questionService.ResolveSelectionDetailed(ctx, selection)
				if err != nil || !result.Settled {
					t.Fatalf("settle before crash: %+v %v", result, err)
				}
			} else {
				err := h.handler.HandleCallback(ctx, callback)
				if (err != nil) != (failure != "bus_outage") {
					t.Fatalf("callback admission: %v", err)
				}
				if failure == "dispatch_error" || failure == "missing_dispatcher" {
					if len(h.channel.answerCalls) != 1 || !h.channel.answerCalls[0].showAlert {
						t.Fatalf("failed admission acknowledged as selected: %+v", h.channel.answerCalls)
					}
				}
			}
			if len(h.dispatcher.commands) != 0 {
				t.Fatal("continuation bypassed durable admission")
			}
			answered, found, err := provider.Questions().GetQuestionByID(ctx, record.QuestionID)
			if err != nil || !found || answered.Status != questioncmd.StatusAnswered {
				t.Fatalf("answer was not durable: %+v %v", answered, err)
			}
			expected, err := h.handler.questionService.ResolveSelectionDetailed(ctx, selection)
			if err != nil || !expected.Settled {
				t.Fatalf("answered selection did not replay: %+v %v", expected, err)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}
			provider, err = state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			bus = &callbackRecoveryBus{}
			d, err = commandoutbox.NewDispatcher(provider.AppKV(), bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			h = newTestHarness(t)
			h.handler.collaboratorStore, _ = newTestCollaboratorStore(auth.TelegramSubject(101))
			h.handler.questionService = questions.New(provider.Questions(), nil, zerolog.Nop())
			h.handler.questionDispatcher = d
			callback.CallbackQueryID = "callback-retry"
			for range 2 {
				if err := h.handler.HandleCallback(ctx, callback); err != nil {
					t.Fatal(err)
				}
				if err := d.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if len(bus.envelopes) != 1 || bus.envelopes[0].ID != expected.Continuation.ID || bus.envelopes[0].DedupeKey != expected.Continuation.DedupeKey {
				t.Fatalf("incorrect recovery: %+v", bus.envelopes)
			}
			current, _, err := provider.Questions().GetQuestionByID(ctx, record.QuestionID)
			if err != nil || current.AnswerJSON != answered.AnswerJSON {
				t.Fatalf("replay changed the saved answer: %+v %v", current, err)
			}
		})
	}
}
