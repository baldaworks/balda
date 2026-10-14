package commandoutboxfx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/handlersfx"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type offlineBus struct{}

func (offlineBus) Dispatch(context.Context, actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	return nil, errors.New("NATS unavailable")
}

type ingressPorts struct {
	fx.In
	Chat     chatapp.Dispatcher                        `name:"command_ingress"`
	Command  commandfx.CommandDispatcher               `name:"command_ingress"`
	Question handlersfx.QuestionContinuationDispatcher `name:"command_ingress"`
}

func TestIngressPortsShareLocalDurableAdmission(t *testing.T) {
	ctx := context.Background()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	var ports ingressPorts
	app := fx.New(fx.NopLogger, Module,
		fx.Provide(func() state.Provider { return provider }, func() *questions.Service { return questions.New(provider.Questions(), nil, zerolog.Nop()) }, func() actortransport.Dispatcher { return offlineBus{} }, zerolog.Nop),
		fx.Invoke(func(p ingressPorts) { ports = p }),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	if any(ports.Chat) != any(ports.Command) {
		t.Fatal("ingress ports use separate durable dispatchers")
	}
	if any(ports.Question) != any(ports.Chat) {
		t.Fatal("question continuations bypass shared durable admission")
	}
	env := actorlayer.Envelope{ID: "inbound", Namespace: "balda", Kind: "turn", From: actorlayer.SystemAddress("ingress"), To: actorlayer.SystemAddress("session"), DedupeKey: "post", Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: []byte(`{"text":"task"}`)}}
	receipt, err := ports.Chat.Dispatch(ctx, env)
	if err != nil || receipt == nil {
		t.Fatalf("conversational admission rejected during NATS outage: %+v %v", receipt, err)
	}
	receipt, err = ports.Command.Dispatch(ctx, env)
	if err != nil || receipt == nil || !receipt.Duplicate {
		t.Fatalf("command admission lost shared receipt: %+v %v", receipt, err)
	}
	receipt, err = ports.Question.Dispatch(ctx, env)
	if err != nil || receipt == nil || !receipt.Duplicate {
		t.Fatalf("question continuation lost shared receipt: %+v %v", receipt, err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := provider.AppKV().List(ctx, "command-outbox/pending/")
	if err != nil || len(pending) != 1 {
		t.Fatalf("shutdown lost pending admission: %v %v", pending, err)
	}
}

func TestChatSettlementRemainsAcceptedWhilePublicationIsUnavailable(t *testing.T) {
	ctx := context.Background()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	var ports ingressPorts
	app := fx.New(fx.NopLogger, Module, fx.Provide(func() state.Provider { return provider }, func() *questions.Service { return questions.New(provider.Questions(), nil, zerolog.Nop()) }, func() actortransport.Dispatcher { return offlineBus{} }, zerolog.Nop), fx.Invoke(func(p ingressPorts) { ports = p }))
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	service, err := chatapp.NewService(chatapp.ServiceParams{Dispatcher: ports.Chat, Sessions: chatapp.SessionPreparerFunc(func(context.Context, chatapp.InboundContext) (chatapp.SessionPreparation, error) {
		return chatapp.SessionPreparation{Ready: true, UserID: "user-1", AgentSessionID: "agent-session"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	request := chatapp.Request{ID: "original-post", Text: "perform task", Locator: deliverycmd.Locator{ChannelType: "mattermost", AddressKey: "channel:thread", SessionID: "session-1"}, UserID: "user-1", ProviderMessageID: "original-post", Direct: true, Source: "mattermost", ReceivedAt: time.Now().UTC()}
	for i := 0; i < 3; i++ {
		result, err := service.HandleChat(ctx, request)
		if err != nil || result.Settlement.Outcome != turncmd.InboundAccepted {
			t.Fatalf("retry/resubmit path after durable admission: %+v %v", result, err)
		}
	}
	keys, err := provider.AppKV().List(ctx, "command-outbox/pending/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("replayed post queued duplicate tasks: %v %v", keys, err)
	}
}

func TestLifecycleRecoversAnsweredQuestionWithoutIngress(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	q := questions.New(provider.Questions(), nil, zerolog.Nop())
	locator := deliverycmd.Locator{SessionID: "session-1", ChannelType: "mattermost", AddressKey: "channel:thread", AddressJSON: `{"channel_id":"channel"}`}
	record, err := q.Ask(ctx, questioncmd.InteractionContext{SessionID: locator.SessionID, Locator: locator}, questioncmd.ResumeTarget{To: "session:session-1"}, questioncmd.Request{Prompt: "Continue?", AllowFreeText: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.BindDelivery(ctx, record.QuestionID, questioncmd.DeliveryRef{Provider: "mattermost", ConversationKey: locator.AddressKey, ProviderMessageID: "prompt"}); err != nil {
		t.Fatal(err)
	}
	result, err := q.ResolveReplyDetailed(ctx, questioncmd.InboundReply{Provider: "mattermost", ConversationKey: locator.AddressKey, ReplyToMessageID: "prompt", MessageID: "answer", Text: "yes"})
	if err != nil || !result.Settled {
		t.Fatalf("answer settlement: %+v %v", result, err)
	}
	app := fx.New(fx.NopLogger, Module, fx.Provide(func() state.Provider { return provider }, func() actortransport.Dispatcher { return offlineBus{} }, func() *questions.Service { return q }, zerolog.Nop))
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop(context.Background()) }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		pending, err := q.PendingQuestionAnswerIDs(ctx, "", 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("Fx lifecycle did not recover persisted answer")
		case <-tick.C:
		}
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := provider.AppKV().List(ctx, "command-outbox/pending/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("shutdown lost recovered continuation: %v %v", keys, err)
	}
}
