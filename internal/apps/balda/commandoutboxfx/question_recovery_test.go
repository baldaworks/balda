package commandoutboxfx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type interruptedAdmissionStore struct {
	commandoutbox.OutboxStore
	commit bool
}

func (s interruptedAdmissionStore) Set(ctx context.Context, key, value string) error {
	if !strings.HasPrefix(key, "command-outbox/pending/") {
		return s.OutboxStore.Set(ctx, key, value)
	}
	if s.commit {
		if err := s.OutboxStore.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return errors.New("interrupted AppKV admission")
}

type recoveredCommandBus struct {
	envelopes []actorlayer.Envelope
}

func (b *recoveredCommandBus) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	b.envelopes = append(b.envelopes, env)
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

func TestAnsweredContinuationRecoversAfterAdmissionFailureAndRestart(t *testing.T) {
	for _, failure := range []string{"full", "write_error", "uncertain_write", "crash_before_enqueue"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "state.db")
			provider, err := state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			questionService := questions.New(provider.Questions(), nil, zerolog.Nop())
			locator := deliverycmd.Locator{ChannelType: "mattermost", AddressKey: "channel:thread", AddressJSON: `{"channel_id":"channel"}`, SessionID: "session-1"}
			record, err := questionService.Ask(ctx, questioncmd.InteractionContext{SessionID: locator.SessionID, Locator: locator}, questioncmd.ResumeTarget{To: "session:session-1"}, questioncmd.Request{Prompt: "Continue?", AllowFreeText: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := questionService.BindDelivery(ctx, record.QuestionID, questioncmd.DeliveryRef{Provider: "mattermost", ConversationKey: locator.AddressKey, ProviderMessageID: "question-post"}); err != nil {
				t.Fatal(err)
			}
			reply := questioncmd.InboundReply{Provider: "mattermost", SessionID: locator.SessionID, ConversationKey: locator.AddressKey, ReplyToMessageID: "question-post", MessageID: "answer-post", Text: "yes", User: questioncmd.UserRef{UserID: "user-1"}}
			request := chatapp.Request{ID: "inbound-1", Locator: locator, Text: reply.Text, UserID: reply.User.UserID, QuestionReply: &reply}
			var store commandoutbox.OutboxStore = provider.AppKV()
			if failure == "write_error" || failure == "uncertain_write" {
				store = interruptedAdmissionStore{OutboxStore: store, commit: failure == "uncertain_write"}
			}
			d, err := commandoutbox.NewDispatcher(store, offlineBus{}, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			if failure == "full" {
				for i := range 256 {
					env := actorlayer.Envelope{ID: fmt.Sprintf("filler-%d", i), Namespace: "test", Kind: "turn", From: actorlayer.SystemAddress("test"), To: actorlayer.SystemAddress("test"), Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: []byte(`{}`)}}
					if _, err := d.Dispatch(ctx, env); err != nil {
						t.Fatal(err)
					}
				}
			}
			if failure == "crash_before_enqueue" {
				resolution, err := questionService.ResolveReplyDetailed(ctx, reply)
				if err != nil || !resolution.Settled {
					t.Fatalf("settle before crash: %+v %v", resolution, err)
				}
			} else {
				service := recoveryChatService(t, questionService, d)
				result, err := service.HandleChat(ctx, request)
				if err == nil || result.Settlement.Outcome != turncmd.InboundRetry {
					t.Fatalf("failure was not retryable: %+v %v", result, err)
				}
			}
			answered, found, err := provider.Questions().GetQuestionByID(ctx, record.QuestionID)
			if err != nil || !found || answered.Status != questioncmd.StatusAnswered {
				t.Fatalf("answer was not persisted: %+v %v", answered, err)
			}
			expected, err := questionService.ResolveReplyDetailed(ctx, reply)
			if err != nil || !expected.Settled {
				t.Fatalf("persisted answer unavailable for replay: %+v %v", expected, err)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}
			provider, err = state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "full" {
				keys, err := provider.AppKV().List(ctx, "command-outbox/pending/")
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range keys {
					if err := provider.AppKV().Delete(ctx, key); err != nil {
						t.Fatal(err)
					}
				}
			}
			bus := &recoveredCommandBus{}
			d, err = commandoutbox.NewDispatcher(provider.AppKV(), bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			questionService = questions.New(provider.Questions(), nil, zerolog.Nop())
			service := recoveryChatService(t, questionService, d)
			// The saved answer, rather than a new ingress ID or changed input,
			// owns continuation identity and content across retries.
			request.ID = "replayed-inbound-2"
			reply.Text = "changed retry text"
			for range 2 {
				result, err := service.HandleChat(ctx, request)
				if err != nil || !result.Activated {
					t.Fatalf("answered reply did not recover: %+v %v", result, err)
				}
				if err := d.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if len(bus.envelopes) != 1 {
				t.Fatalf("continuations published = %d, want 1", len(bus.envelopes))
			}
			env := bus.envelopes[0]
			if env.ID != expected.Continuation.ID || env.DedupeKey != "question:"+record.QuestionID+":answered" {
				t.Fatalf("unstable continuation identity: %+v", env)
			}
			var payload questioncmd.AnsweredContinuation
			err = actorlayer.UnmarshalPayload(env.Payload, &payload)
			if err != nil || payload.Answer.Text != "yes" || payload.QuestionID != record.QuestionID {
				t.Fatalf("persisted answer was replaced: %+v %v", payload, err)
			}
		})
	}
}

func recoveryChatService(t *testing.T, q *questions.Service, dispatcher chatapp.Dispatcher) *chatapp.Service {
	t.Helper()
	service, err := chatapp.NewService(chatapp.ServiceParams{Questions: chatfx.NewQuestionAdapter(q), Dispatcher: dispatcher, Sessions: chatapp.SessionPreparerFunc(func(context.Context, chatapp.InboundContext) (chatapp.SessionPreparation, error) {
		t.Error("answered reply fell through to an ordinary turn")
		return chatapp.SessionPreparation{}, errors.New("unexpected ordinary turn")
	})})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
