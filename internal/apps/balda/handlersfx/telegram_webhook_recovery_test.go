package handlersfx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldachannel "github.com/baldaworks/balda/internal/apps/balda/channel"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/questionoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

const (
	webhookStoreError        = "store_error"
	webhookCrash             = "crash"
	webhookMarkerWriteError  = "marker_write_error"
	webhookMarkerCommitError = "marker_commit_error"
	webhookCleanupError      = "cleanup_store_error"
)

type webhookAdmissionStore struct {
	commandoutbox.OutboxStore
	failure       string
	attempted     chan struct{}
	once          sync.Once
	admitted      *atomic.Bool
	earlyControls *atomic.Bool
}

func (s *webhookAdmissionStore) Set(ctx context.Context, key, value string) error {
	var record struct {
		Envelope string `json:"envelope"`
	}
	_ = json.Unmarshal([]byte(value), &record)
	env, _ := actorlayer.DecodeEnvelope(record.Envelope)
	answer := env.Kind == actorcmd.KindQuestionAnswered
	if answer && (s.failure == webhookStoreError || s.failure == webhookCrash) {
		s.once.Do(func() { close(s.attempted) })
		if s.failure == webhookCrash {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("injected continuation admission failure")
	}
	if strings.HasPrefix(env.DedupeKey, "question:q1:controls:clear:") && !s.admitted.Load() {
		s.earlyControls.Store(true)
	}
	if strings.HasPrefix(env.DedupeKey, "question:q1:controls:clear:") && s.failure == webhookCleanupError {
		return errors.New("injected cleanup admission failure")
	}
	if err := s.OutboxStore.Set(ctx, key, value); err != nil {
		return err
	}
	if answer {
		s.admitted.Store(true)
		if s.attempted != nil {
			s.once.Do(func() { close(s.attempted) })
		}
	}
	return nil
}

type interruptedQuestionMarker struct {
	state.QuestionStore
	commit bool
}

func (s interruptedQuestionMarker) MarkQuestionAnswerAdmitted(ctx context.Context, id string) error {
	if s.commit {
		if err := s.QuestionStore.MarkQuestionAnswerAdmitted(ctx, id); err != nil {
			return err
		}
	}
	return errors.New("interrupted answer admission marker")
}

type webhookRecoveryBus struct {
	mu       sync.Mutex
	answers  []string
	delivery deliveryworkflow.Dispatcher
}

func (b *webhookRecoveryBus) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if env.Kind == actorcmd.KindQuestionAnswered {
		b.mu.Lock()
		b.answers = append(b.answers, env.ID)
		b.mu.Unlock()
	} else {
		var payload deliverycmd.Payload
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			return nil, err
		}
		if _, err := b.delivery.Dispatch(ctx, deliveryworkflow.Delivery{Payload: payload}); err != nil {
			return nil, err
		}
	}
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

func TestTelegramWebhookSingleUpdateRecoversAnswerWithoutRedelivery(t *testing.T) {
	for _, kind := range []string{"callback", "text"} {
		for _, failure := range []string{webhookStoreError, webhookCrash, webhookMarkerWriteError, webhookMarkerCommitError, webhookCleanupError} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				ctx := t.Context()
				var admitted, earlyControls atomic.Bool
				var cleared, selected, updateCount atomic.Int32
				apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch strings.ToLower(filepath.Base(r.URL.Path)) {
					case "getme":
						_, _ = w.Write([]byte(`{"ok":true,"result":{"id":4242,"is_bot":true,"first_name":"Test","username":"testbot"}}`))
					case "answercallbackquery":
						var body struct {
							Text string `json:"text"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						if strings.Contains(strings.ToLower(body.Text), "try again") {
							t.Error("saved answer asks for a duplicate inbound")
						}
						if body.Text == questionCallbackSelectedMessage {
							selected.Add(1)
							if !admitted.Load() {
								t.Error("callback confirmed before durable admission")
							}
						}
						_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
					case "editmessagereplymarkup":
						if !admitted.Load() {
							earlyControls.Store(true)
						}
						cleared.Add(1)
						_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
					case "sendmessage":
						_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":44,"date":1,"chat":{"id":9001,"type":"private"},"text":"selection"}}`))
					default:
						_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
					}
				}))
				defer apiServer.Close()
				api, err := client.NewClientWithResponses(apiServer.URL)
				if err != nil {
					t.Fatal(err)
				}
				adapter := baldatelegram.NewAdapter(baldatelegram.AdapterParams{TGClient: api, Messenger: baldatelegram.NewMessenger(api, zerolog.Nop()), Logger: zerolog.Nop()})
				bus := &webhookRecoveryBus{delivery: deliveryfx.NewChannelDispatcher(baldachannel.NewRouter(map[string]deliverycmd.Adapter{"telegram": adapter}))}
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
				locator := baldatelegram.NewLocator(9001, 0)
				record := testTelegramQuestionRecord(t, 101)
				record.ChannelKind, record.AddressKey, record.AddressJSON, record.Prompt = "telegram", locator.AddressKey, locator.AddressJSON, "Allow?"
				if err := provider.Questions().CreatePendingQuestion(ctx, record); err != nil {
					t.Fatal(err)
				}
				faultStore := &webhookAdmissionStore{OutboxStore: provider.AppKV(), failure: failure, attempted: make(chan struct{}), admitted: &admitted, earlyControls: &earlyControls}
				d, err := commandoutbox.NewDispatcher(faultStore, bus, zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				var questionStore questions.Store = provider.Questions()
				if failure == webhookMarkerWriteError || failure == webhookMarkerCommitError {
					questionStore = interruptedQuestionMarker{QuestionStore: provider.Questions(), commit: failure == webhookMarkerCommitError}
				}
				q := questions.New(questionStore, nil, zerolog.Nop())
				q.SetControlPublisher(deliveryfx.NewQuestionControlPublisher(d))
				h := newTestHarness(t)
				h.handler.collaboratorStore, _ = newTestCollaboratorStore(auth.TelegramSubject(101))
				h.handler.questionService, h.handler.questionDispatcher, h.handler.channel = q, d, adapter
				server := baldatelegram.NewServer(baldatelegram.ServerParams{Channel: adapter, TGClient: api, InboundHandler: h.handler, TelegramEnabled: true, Logger: zerolog.Nop()})
				cfg := tgbotkit.Config{Token: "local-test-only", Webhook: tgbotkit.WebhookConfig{Enabled: true, Path: "/telegram/webhook", URL: "https://example.test/telegram/webhook", AuthToken: "local-secret"}}
				source, err := tgbotkit.NewUpdateSource(cfg, api, nil, zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				shared := source.(tgbotkit.SharedWebhookSource)
				if err := shared.UseSharedListener(); err != nil {
					t.Fatal(err)
				}
				handler, callbackPath := shared.HTTPCallback()
				mux := http.NewServeMux()
				mux.Handle(callbackPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { updateCount.Add(1); handler.ServeHTTP(w, r) }))
				webhookServer := httptest.NewServer(mux)
				defer webhookServer.Close()
				body := `{"update_id":7,"callback_query":{"id":"only-callback","from":{"id":101,"is_bot":false,"first_name":"User"},"chat_instance":"test","message":{"message_id":42,"date":1,"chat":{"id":9001,"type":"private"}},"data":"balda:q:q1:1"}}`
				if kind == "text" {
					body = `{"update_id":7,"message":{"message_id":43,"date":1,"chat":{"id":9001,"type":"private"},"from":{"id":101,"is_bot":false,"first_name":"User"},"text":"allow","reply_to_message":{"message_id":42,"date":1,"from":{"id":4242,"is_bot":true,"first_name":"Test","username":"testbot"},"chat":{"id":9001,"type":"private"}}}}`
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookServer.URL+callbackPath, bytes.NewBufferString(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "local-secret")
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("webhook status = %d", response.StatusCode)
				}
				before, _, err := provider.Questions().GetQuestionByID(ctx, "q1")
				if err != nil || before.Status != questioncmd.StatusPending {
					t.Fatalf("HTTP 200 did not precede handler: %+v %v", before, err)
				}
				bot, err := tgbotkit.NewBot(cfg, api, source, zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				newTelegramServerHandlerAdapter(server).Register(bot.Handlers())
				botCtx, stopBot := context.WithCancel(ctx)
				defer stopBot()
				botDone := make(chan error, 1)
				go func() { botDone <- bot.Run(botCtx) }()
				select {
				case <-faultStore.attempted:
				case <-time.After(10 * time.Second):
					t.Fatal("webhook answer never reached admission")
				}
				stopBot()
				select {
				case err := <-botDone:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("bot did not stop")
				}
				if failure == webhookMarkerWriteError || failure == webhookMarkerCommitError || failure == webhookCleanupError {
					recovery, err := questionoutbox.New(q, d, zerolog.Nop())
					if err != nil {
						t.Fatal(err)
					}
					if err := recovery.Flush(ctx); err == nil {
						t.Fatal("injected post-admission failure was not observed")
					}
				}
				if earlyControls.Load() || cleared.Load() != 0 || (selected.Load() != 0 && !admitted.Load()) {
					t.Fatal("controls/positive callback escaped before admission")
				}
				if len(h.dispatcher.commands) != 0 {
					t.Fatal("durably saved answer fell through to a raw message/retry path")
				}
				pending, err := q.PendingQuestionAnswerIDs(ctx, "", 32)
				if err != nil || len(pending) != 1 {
					t.Fatalf("answer recovery marker lost: %v %v", pending, err)
				}
				if err := provider.Close(); err != nil {
					t.Fatal(err)
				}
				provider, err = state.NewSQLiteProvider(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				// No bot, webhook replay, callback or inbound is started after reopen.
				observer := &webhookAdmissionStore{OutboxStore: provider.AppKV(), admitted: &admitted, earlyControls: &earlyControls}
				d, err = commandoutbox.NewDispatcher(observer, bus, zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				q = questions.New(provider.Questions(), nil, zerolog.Nop())
				q.SetControlPublisher(deliveryfx.NewQuestionControlPublisher(d))
				recovery, err := questionoutbox.New(q, d, zerolog.Nop())
				if err != nil {
					t.Fatal(err)
				}
				d.Start()
				recovery.Start()
				defer func() { _ = recovery.Stop(context.Background()); _ = d.Stop(context.Background()) }()
				deadline := time.NewTimer(15 * time.Second)
				defer deadline.Stop()
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					pending, err = q.PendingQuestionAnswerIDs(ctx, "", 32)
					if err != nil {
						t.Fatal(err)
					}
					if len(pending) == 0 && cleared.Load() == 1 {
						break
					}
					select {
					case <-deadline.C:
						t.Fatal("autonomous recovery did not complete")
					case <-ticker.C:
					}
				}
				if err := recovery.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				if err := d.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				bus.mu.Lock()
				answers := append([]string(nil), bus.answers...)
				bus.mu.Unlock()
				if len(answers) != 1 || earlyControls.Load() || updateCount.Load() != 1 {
					t.Fatalf("recovery answers=%v earlyControls=%v updates=%d", answers, earlyControls.Load(), updateCount.Load())
				}
				var result questioncmd.AnsweredContinuation
				saved, _, err := provider.Questions().GetQuestionByID(ctx, "q1")
				if err != nil || saved.AnswerContinuationPending || saved.AnswerControlsPending {
					t.Fatalf("handoff incomplete: %+v %v", saved, err)
				}
				if err := json.Unmarshal([]byte(saved.AnswerJSON), &result.Answer); err != nil {
					t.Fatal(err)
				}
				if result.Answer.SelectedOption != "allow" {
					t.Fatalf("saved answer=%+v", result.Answer)
				}
			})
		}
	}
}
