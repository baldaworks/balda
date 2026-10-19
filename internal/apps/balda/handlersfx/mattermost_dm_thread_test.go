package handlersfx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/mattermostfx"
	"github.com/baldaworks/balda/internal/apps/balda/chatfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissions"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type dmThreadDispatcher struct{ messages chan actorlayer.Envelope }

func (d dmThreadDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.messages <- env
	return &actortransport.DispatchReceipt{}, nil
}

func TestMattermostDMRepliesStayInUserMessageThread(t *testing.T) {
	const botID = "balda-bot"
	const requesterID = "owner-raw-id"
	const allowLabel = "Allow once"
	const allowID = "allow"
	const threadRoot = "user-message-root"
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := questions.New(provider.Questions(), nil, zerolog.Nop())
	dispatcher := dmThreadDispatcher{messages: make(chan actorlayer.Envelope, 4)}
	registry, err := permissionfmt.NewStructuredRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := mattermostfx.NewPermissionStructuredRegistrar()(registry); err != nil {
		t.Fatal(err)
	}
	review := permissions.New(permissions.Config{Mode: permissioncmd.ModeAsk, Timeout: 10 * time.Second}, service, dispatcher, registry, zerolog.Nop())
	channel := mattermost.Channel{ID: "private-channel", Type: "D"}
	locator := mattermost.LocatorForPost(channel, mattermost.Post{ID: threadRoot, ChannelID: channel.ID, UserID: requesterID})
	outcome := make(chan permissioncmd.Decision, 1)
	failures := make(chan error, 1)
	go func() {
		d, e := review.Review(ctx, permissioncmd.Request{Interaction: questioncmd.InteractionContext{SessionID: locator.SessionID, ChannelKind: "mattermost", Locator: locator, RequestedBy: questioncmd.UserRef{UserID: requesterID}}, ToolCall: permissioncmd.ToolCall{Title: "Approve fixture operation"}, Options: []permissioncmd.Option{{ID: allowID, Name: allowLabel, Kind: "allow_once"}, {ID: "deny", Name: "Decline", Kind: "reject_once"}}})
		outcome <- d
		failures <- e
	}()
	var envelope actorlayer.Envelope
	select {
	case envelope = <-dispatcher.messages:
	case <-time.After(5 * time.Second):
		t.Fatal("permission was not delivered")
	}
	var payload deliverycmd.Payload
	if err = actorlayer.UnmarshalPayload(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var posts []mattermost.Post
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/posts" {
			http.NotFound(w, r)
			return
		}
		var p mattermost.Post
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		p.ID = fmt.Sprintf("bot-post-%d", len(posts)+1)
		p.UserID = botID
		posts = append(posts, p)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)
	}))
	t.Cleanup(api.Close)
	adapter := mattermost.NewAdapter(mattermost.NewClient(api.URL, "fixture-token", botID), zerolog.Nop())
	if _, err := adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Text: "Ordinary reply"}); err != nil {
		t.Fatal(err)
	}
	delivered, err := adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Text: payload.Text, DeliveryFormat: payload.DeliveryFormat, Question: payload.Question})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	created := append([]mattermost.Post(nil), posts...)
	mu.Unlock()
	if len(created) != 2 || created[0].RootID != threadRoot || created[1].RootID != threadRoot {
		t.Fatalf("both ordinary reply and question must use the user message thread: %+v", created)
	}
	if delivered.ProviderMessageID != threadRoot {
		t.Fatalf("binding=%q, want user message thread root", delivered.ProviderMessageID)
	}

	if err = service.BindDelivery(ctx, payload.Question.ID, questioncmd.DeliveryRef{Provider: "mattermost", ConversationKey: locator.AddressKey, ProviderMessageID: delivered.ProviderMessageID}); err != nil {
		t.Fatal(err)
	}
	replies := 0
	reply := func(user string) questioncmd.InboundReply {
		replies++
		post := mattermost.Post{ID: fmt.Sprintf("reply-%d-%s", replies, user), ChannelID: channel.ID, UserID: user, RootID: threadRoot}
		request := mattermostChatRequest(mattermost.InboundMessage{Locator: mattermost.LocatorForPost(channel, post), PostID: post.ID, SenderID: user, Text: "1", Direct: true, RootID: post.RootID}, time.Now())
		if request.QuestionReply == nil {
			t.Fatal("native threaded reply was not recognized")
		}
		return *request.QuestionReply
	}
	resolver := chatfx.NewQuestionAdapter(service)
	wrong := reply("another-user")
	rejected, err := resolver.ResolveQuestionReply(ctx, wrong)
	if err != nil || !rejected.Matched || rejected.Settled {
		t.Fatalf("foreign responder=%+v err=%v", rejected, err)
	}
	correct := reply(requesterID)
	resolved, err := resolver.ResolveQuestionReply(ctx, correct)
	if err != nil || !resolved.Matched || !resolved.Settled {
		t.Fatalf("native owner reply=%+v err=%v", resolved, err)
	}
	var answer questioncmd.AnsweredContinuation
	if err = actorlayer.UnmarshalPayload(resolved.Continuation.Payload, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Answer.SelectedOption != allowID {
		t.Fatalf("answer=%+v", answer)
	}
	review.Resolve(answer.QuestionID, permissioncmd.Decision{OptionID: answer.Answer.SelectedOption, Source: "user"})
	if err = <-failures; err != nil {
		t.Fatal(err)
	}
	if got := <-outcome; got.OptionID != allowID {
		t.Fatalf("permission decision=%+v", got)
	}
	stored, ok, err := provider.Questions().GetQuestionByID(ctx, answer.QuestionID)
	if err != nil || !ok || stored.Status != questioncmd.StatusAnswered {
		t.Fatalf("stored=%+v found=%v err=%v", stored, ok, err)
	}
	second, err := service.Ask(ctx, questioncmd.InteractionContext{SessionID: locator.SessionID, Locator: locator, RequestedBy: questioncmd.UserRef{UserID: requesterID}}, questioncmd.ResumeTarget{To: "session:" + locator.SessionID}, questioncmd.Request{Prompt: "Next operation?", Timeout: time.Minute, Responder: questioncmd.ResponderRequester, Options: []questioncmd.Option{{ID: allowID, Label: allowLabel}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BindDelivery(ctx, second.QuestionID, questioncmd.DeliveryRef{Provider: "mattermost", ConversationKey: locator.AddressKey, ProviderMessageID: threadRoot}); err != nil {
		t.Fatal(err)
	}
	replayed, err := resolver.ResolveQuestionReply(ctx, correct)
	if err != nil || !replayed.Matched || replayed.Settled {
		t.Fatalf("old reply must not answer the next question: %+v error=%v", replayed, err)
	}
	current, err := resolver.ResolveQuestionReply(ctx, reply(requesterID))
	if err != nil || !current.Settled {
		t.Fatalf("second question=%+v error=%v", current, err)
	}
	var nextAnswer questioncmd.AnsweredContinuation
	if err := actorlayer.UnmarshalPayload(current.Continuation.Payload, &nextAnswer); err != nil {
		t.Fatal(err)
	}
	if nextAnswer.QuestionID != second.QuestionID {
		t.Fatalf("answered %q, want current question %q", nextAnswer.QuestionID, second.QuestionID)
	}

	for range 2 {
		pending, err := service.Ask(ctx, questioncmd.InteractionContext{SessionID: locator.SessionID, Locator: locator, RequestedBy: questioncmd.UserRef{UserID: requesterID}}, questioncmd.ResumeTarget{To: "session:" + locator.SessionID}, questioncmd.Request{Prompt: "Concurrent operation?", Timeout: time.Minute, Responder: questioncmd.ResponderRequester, Options: []questioncmd.Option{{ID: allowID, Label: allowLabel}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.BindDelivery(ctx, pending.QuestionID, questioncmd.DeliveryRef{Provider: "mattermost", ConversationKey: locator.AddressKey, ProviderMessageID: threadRoot}); err != nil {
			t.Fatal(err)
		}
	}
	ambiguous, err := resolver.ResolveQuestionReply(ctx, reply(requesterID))
	if err == nil || ambiguous.Settled {
		t.Fatalf("ambiguous thread must not approve an arbitrary operation: %+v error=%v", ambiguous, err)
	}
}
