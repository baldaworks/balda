package telegram

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	adksession "google.golang.org/adk/v2/session"
)

const testServerBotUsername = "testbot"

type fakeSessionStore struct {
	record         baldastate.SessionRecord
	foundByAddress bool
	lastUpsert     baldastate.SessionRecord
}

func (f *fakeSessionStore) Upsert(_ context.Context, record baldastate.SessionRecord) error {
	f.lastUpsert = record
	f.record = record
	f.foundByAddress = true
	return nil
}

func (f *fakeSessionStore) GetByAddress(_ context.Context, channelType, addressKey string) (baldastate.SessionRecord, bool, error) {
	if !f.foundByAddress {
		return baldastate.SessionRecord{}, false, nil
	}
	if f.record.ChannelType != channelType || f.record.AddressKey != addressKey {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (f *fakeSessionStore) GetBySessionID(_ context.Context, sessionID string) (baldastate.SessionRecord, bool, error) {
	if !f.foundByAddress || f.record.SessionID != sessionID {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (*fakeSessionStore) DeleteBySessionID(context.Context, string) error { return nil }
func (f *fakeSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	if !f.foundByAddress {
		return nil, nil
	}
	return []baldastate.SessionRecord{f.record}, nil
}

type fakeAgentBuilder struct{ metadata baldasession.AgentMetadata }

func (f *fakeAgentBuilder) CreateRuntimeSession(
	context.Context,
	*baldasession.BuiltRuntime,
	string,
	string,
	string,
	string,
	baldasession.RuntimeSessionContext,
) (adksession.Session, error) {
	return nil, nil
}

func (f *fakeAgentBuilder) GetAgentMetadata(string) baldasession.AgentMetadata { return f.metadata }

type fakeRuntimeManager struct{ providerID string }

func (*fakeRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return &baldasession.BuiltRuntime{}, nil
}

func (f *fakeRuntimeManager) ProviderID() string { return f.providerID }

func newServerOwnerStore(t *testing.T) *auth.OwnerStore {
	t.Helper()
	store, err := auth.NewOwnerStore(&fakeOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := store.RegisterOwner(101, 9001); err != nil {
		t.Fatalf("RegisterOwner() error = %v", err)
	}
	return store
}

func newServerMessageHarness(t *testing.T, topicID int) (*Server, *fakeTurnDispatcher, baldasession.SessionLocator) {
	t.Helper()
	locator := NewLocator(9001, topicID)
	store := &fakeSessionStore{}
	sessionManager := newSessionManagerHarness(t, store)
	dispatcher := &fakeTurnDispatcher{}
	tgClient := &fakeTelegramClient{}
	server := &Server{
		ownerStore:      newServerOwnerStore(t),
		channel:         newTestAdapter(tgClient, "none"),
		sessionManager:  sessionManager,
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
		now:             func() time.Time { return time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC) },
	}
	server.setOwner(101, 9001)
	server.botUsername = testServerBotUsername
	server.botUserID = 4242
	return server, dispatcher, locator
}

func newSessionManagerHarness(t *testing.T, store *fakeSessionStore) *baldasession.Manager {
	t.Helper()
	m := &baldasession.Manager{}
	setUnexportedField(t, m, "agentBuilder", &fakeAgentBuilder{
		metadata: baldasession.AgentMetadata{
			Type:       "opencode_acp",
			Model:      "gpt-5",
			MCPServers: []string{"provider_mcp"},
		},
	})
	setUnexportedField(t, m, "runtimeManager", &fakeRuntimeManager{providerID: "alpha"})
	setUnexportedField(t, m, "baldaProviderName", "alpha")
	setUnexportedField(t, m, "sessionStore", store)
	setUnexportedField(t, m, "logger", zerolog.Nop())
	setUnexportedField(t, m, "sessions", map[string]*baldasession.TopicSession{})
	return m
}

func newSessionManagerWithSession(t *testing.T, locator baldasession.SessionLocator, ts *baldasession.TopicSession) *baldasession.Manager {
	t.Helper()
	m := &baldasession.Manager{}
	setUnexportedField(t, m, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: ts})
	setUnexportedField(t, m, "sessionStore", &fakeSessionStore{})
	return m
}

func newTopicSession(t *testing.T, sessionID string) *baldasession.TopicSession {
	t.Helper()
	ts := &baldasession.TopicSession{}
	setUnexportedField(t, ts, "sessionID", sessionID)
	return ts
}

func setUnexportedField[T any](t *testing.T, target any, fieldName string, value T) {
	t.Helper()
	rv := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func TestServerHandleMessage_IgnoresNilFrom(t *testing.T) {
	server, _, _ := newServerMessageHarness(t, 0)
	text := "hello"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			Text: &text,
			From: nil,
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
}

func TestServerHandleMessage_ChannelIgnoresNonMention(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "hello world"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "supergroup"},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_ChannelMentionBypassesGate(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "@testbot hello world"
	entities := []client.MessageEntity{{Type: "mention", Offset: 0, Length: len("@testbot")}}
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:     client.Chat{Id: 9001, Type: "supergroup"},
			Text:     &text,
			Entities: &entities,
			From:     &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	if got := baldaexecution.EnvelopeSessionID(sessionEnv); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestServerHandleMessage_DMNonMentionAllowed(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "hello from dm"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	if got := baldaexecution.EnvelopeSessionID(sessionEnv); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestServerHandleMessage_CommandRemainsOnCommandPath(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "/reset"
	entities := []client.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(text)}}
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			MessageId: 56,
			Text:      &text,
			Entities:  &entities,
			From:      &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published conversational commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_UnauthorizedUserRemainsSilent(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "try to enter the owner session"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			MessageId: 55,
			Text:      &text,
			From:      &client.User{Id: 202},
		},
	}
	if err := server.HandleMessage(t.Context(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if got := len(turns.commandSnapshot()); got != 0 {
		t.Fatalf("published commands = %d, want 0", got)
	}
}

func TestServerHandleMessage_RejectsFalsePositiveBotMentionPrefix(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "@testbotx please ignore this"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "supergroup"},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_TopicUnknownThreadIgnoresNonMentionNonReply(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 77)
	text := "hello from the topic"
	topicID := 99
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			Text:            &text,
			From:            &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_ChannelReplyToBotBypassesMentionGate(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "following up in channel"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:           client.Chat{Id: 9001, Type: "supergroup"},
			Text:           &text,
			From:           &client.User{Id: 101},
			ReplyToMessage: replyToMessageFrom(4242, true),
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	if got := baldaexecution.EnvelopeSessionID(sessionEnv); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestServerHandleMessage_ForwardedBotMessageAddsForwardedContextToPublishedCommand(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "проверь этот коммит"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "supergroup"},
			Text: &text,
			From: &client.User{Id: 101},
			ForwardOrigin: &client.MessageOrigin{
				"type": "user",
				"user": map[string]interface{}{
					"id":     float64(4242),
					"is_bot": true,
				},
			},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if !strings.Contains(payload.Text, "Forwarded context:\n"+text) {
		t.Fatalf("payload text = %q, want forwarded context block", payload.Text)
	}
}

func TestServerHandleMessage_PublishesDirectSessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "run tests"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	if sessionEnv.To.Target != baldaexecution.ActorTypeSession {
		t.Fatalf("command target = %q, want %q", sessionEnv.To.Target, baldaexecution.ActorTypeSession)
	}
	if got := baldaexecution.EnvelopeSessionID(sessionEnv); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if payload.Source != "telegram" || !payload.Deliver {
		t.Fatalf("session turn payload = %+v, want telegram deliver=true", payload)
	}
}

func TestServerStartRestoresPersistedOwnerForDirectMessages(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	server.ownerID = 0
	server.chatID = 0
	server.tgClient = &fakeTelegramClient{}
	server.telegramConfigured = true
	server.telegramEnabled = true

	if err := server.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	text := "run tests after restart"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(t.Context(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if _, ok := findSessionEnvelope(turns.commands, locator.SessionID); !ok {
		t.Fatalf("session command for %q not found after restart in %+v", locator.SessionID, turns.commands)
	}
}

func TestServerHandleMessage_ReplyAddsReplyContextToPublishedCommand(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "сделай пр"
	replyText := "проверь этот коммит"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:           client.Chat{Id: 9001, Type: "private"},
			Text:           &text,
			From:           &client.User{Id: 101},
			ReplyToMessage: replyToMessageWithTextFrom(777, false, replyText),
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, "tg-9001-0")
	if !ok {
		t.Fatalf("session command not found in %+v", turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if !strings.Contains(payload.Text, "Reply context:\n"+replyText) {
		t.Fatalf("payload text = %q, want reply context block", payload.Text)
	}
	if !strings.Contains(payload.Text, "User message:\n"+text) {
		t.Fatalf("payload text = %q, want user message block", payload.Text)
	}
}

func TestServerHandleMessage_PublishesAttachmentOnlySessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	size := 81375
	event := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			Photo: &[]client.PhotoSize{{
				FileId:       "photo-file-id",
				FileUniqueId: "photo-unique-id",
				FileSize:     &size,
				Width:        900,
				Height:       896,
			}},
			From: &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != attachment.KindPhoto {
		t.Fatalf("attachments = %+v, want one photo", payload.Attachments)
	}
	if !strings.Contains(payload.Text, "Attachment manifest:") || !strings.Contains(payload.Text, "kind: photo") {
		t.Fatalf("payload text = %q, want photo attachment manifest", payload.Text)
	}
}

func TestServerHandleMessage_PublicAttachmentWithoutMentionOrReplyIsIgnored(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 77)
	topicID := 77
	isTopicMessage := true
	fileName := "context.json"
	event := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			From:            &client.User{Id: 101},
			Document:        &client.Document{FileId: "document-file-id", FileName: &fileName},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_PublicAttachmentWithMentionPublishesTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 77)
	topicID := 77
	isTopicMessage := true
	fileName := "context.json"
	caption := "@testbot review this"
	captionEntities := []client.MessageEntity{{Type: "mention", Offset: 0, Length: len("@testbot")}}
	event := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			From:            &client.User{Id: 101},
			Caption:         &caption,
			CaptionEntities: &captionEntities,
			Document:        &client.Document{FileId: "document-file-id", FileName: &fileName},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != attachment.KindDocument {
		t.Fatalf("attachments = %+v, want one document", payload.Attachments)
	}
	if !strings.Contains(payload.Text, "review this") || !strings.Contains(payload.Text, "name: "+fileName) {
		t.Fatalf("payload text = %q, want mention text and document manifest", payload.Text)
	}
}

func TestServerHandleMessage_TopicKnownThreadStillRequiresMentionOrReply(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 77)
	text := "hello from the topic"
	topicID := 77
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			Text:            &text,
			From:            &client.User{Id: 101},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestServerHandleMessage_TopicReplyToBotBypassesMentionGate(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 77)
	text := "topic follow up"
	topicID := 77
	isTopicMessage := true
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			Text:            &text,
			From:            &client.User{Id: 101},
			ReplyToMessage:  replyToMessageFrom(4242, true),
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	if got := baldaexecution.EnvelopeSessionID(sessionEnv); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestServerHandleMessage_ChannelReplyToDifferentBotIgnored(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	text := "following up in channel"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:           client.Chat{Id: 9001, Type: "supergroup"},
			Text:           &text,
			From:           &client.User{Id: 101},
			ReplyToMessage: replyToMessageFrom(9898, true),
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

type recordingAttachmentStore struct{ calls [][]attachment.Descriptor }

func (s *recordingAttachmentStore) PersistTelegram(_ context.Context, in []attachment.Descriptor) ([]attachment.Descriptor, error) {
	s.calls = append(s.calls, append([]attachment.Descriptor(nil), in...))
	return attachment.NormalizeList(in), nil
}

func TestServerHandleMessage_CoalescesTelegramMediaGroupIntoOneTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	turns.commandSignal = make(chan struct{}, 4)
	attachmentStore := &recordingAttachmentStore{}
	server.attachmentStore = attachmentStore
	mediaGroupID := "album-42"
	caption := "review both files"
	photoSize := 1024
	photoEvent := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    101,
			MediaGroupId: &mediaGroupID,
			Caption:      &caption,
			Photo: &[]client.PhotoSize{{
				FileId:       "photo-file-id",
				FileUniqueId: "photo-unique-id",
				FileSize:     &photoSize,
			}},
		},
	}
	documentName := "notes.txt"
	documentEvent := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    102,
			MediaGroupId: &mediaGroupID,
			Document: &client.Document{
				FileId:       "document-file-id",
				FileUniqueId: "document-unique-id",
				FileName:     &documentName,
			},
		},
	}
	if err := server.HandleMessage(t.Context(), photoEvent); err != nil {
		t.Fatalf("HandleMessage(photo) error = %v", err)
	}
	if err := server.HandleMessage(t.Context(), documentEvent); err != nil {
		t.Fatalf("HandleMessage(document) error = %v", err)
	}
	deadline := time.After(2 * time.Second)
	var commands []actorlayer.Envelope
	for len(commands) == 0 {
		select {
		case <-turns.commandSignal:
			commands = sessionEnvelopesFor(locator.SessionID, turns.commandSnapshot())
		case <-deadline:
			t.Fatal("timed out waiting for media-group session turn")
		}
	}
	if len(commands) != 1 {
		t.Fatalf("session commands = %d, want 1", len(commands))
	}
	if commands[0].DedupeKey != "telegram:9001:0:user:101:media-group:album-42" {
		t.Fatalf("dedupe key = %q", commands[0].DedupeKey)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(payload.Attachments))
	}
	if len(attachmentStore.calls) != 1 || len(attachmentStore.calls[0]) != 2 {
		t.Fatalf("attachment store calls = %+v", attachmentStore.calls)
	}
}

func TestServerHandleMessage_PublishesVoiceOnlySessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	size := int64(4096)
	event := &events.MessageEvent{
		Type: messagetype.Voice,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			From: &client.User{Id: 101},
			Voice: &client.Voice{
				FileId:       "voice-file-id",
				FileUniqueId: "voice-unique-id",
				FileSize:     &size,
			},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != attachment.KindVoice {
		t.Fatalf("attachments = %+v, want one voice", payload.Attachments)
	}
}

func TestServerHandleMessage_FlushesMediaGroupBeforeFollowingMessage(t *testing.T) {
	server, turns, _ := newServerMessageHarness(t, 0)
	mediaGroupID := "album-42"
	photoEvent := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    101,
			MediaGroupId: &mediaGroupID,
			Photo:        &[]client.PhotoSize{{FileId: "photo-file-id"}},
		},
	}
	text := "use the attached file"
	textEvent := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
			MessageId: 102,
			Text:      &text,
		},
	}
	if err := server.HandleMessage(t.Context(), photoEvent); err != nil {
		t.Fatalf("HandleMessage(photo) error = %v", err)
	}
	if err := server.HandleMessage(t.Context(), textEvent); err != nil {
		t.Fatalf("HandleMessage(text) error = %v", err)
	}
	commands := sessionEnvelopesFor("tg-9001-0", turns.commandSnapshot())
	if len(commands) != 2 {
		t.Fatalf("published session commands = %d, want media group then text", len(commands))
	}
	var first turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[0].Payload, &first); err != nil {
		t.Fatalf("decode first session turn payload: %v", err)
	}
	var second turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[1].Payload, &second); err != nil {
		t.Fatalf("decode second session turn payload: %v", err)
	}
	if len(first.Attachments) != 1 || first.Attachments[0].FileID != "photo-file-id" {
		t.Fatalf("first turn attachments = %+v, want media group", first.Attachments)
	}
	if second.Text != text || len(second.Attachments) != 0 {
		t.Fatalf("second turn = %+v, want following text without attachments", second)
	}
}

func TestServerHandleMessage_PublishesAudioOnlySessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	fileName := "sample.mp3"
	mimeType := "audio/mpeg"
	size := int64(8192)
	event := &events.MessageEvent{
		Type: messagetype.Audio,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "private"},
			From: &client.User{Id: 101},
			Audio: &client.Audio{
				FileId:       "audio-file-id",
				FileUniqueId: "audio-unique-id",
				FileName:     &fileName,
				MimeType:     &mimeType,
				FileSize:     &size,
			},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(payload.Attachments))
	}
	audio := payload.Attachments[0]
	if audio.Kind != attachment.KindDocument || audio.FileID != "audio-file-id" || audio.FileName != fileName || audio.MIMEType != mimeType || audio.SizeBytes != size {
		t.Fatalf("audio attachment = %+v", audio)
	}
	if !strings.Contains(payload.Text, "Attachment manifest:") || !strings.Contains(payload.Text, "kind: document") || !strings.Contains(payload.Text, "file_name: sample.mp3") {
		t.Fatalf("payload text = %q, want audio attachment manifest", payload.Text)
	}
}

func TestServerHandleMessage_PublishesCaptionAndAudioInOneSessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	caption := "summarize this audio"
	event := &events.MessageEvent{
		Type: messagetype.Audio,
		Message: &client.Message{
			Chat:    client.Chat{Id: 9001, Type: "private"},
			From:    &client.User{Id: 101},
			Caption: &caption,
			Audio:   &client.Audio{FileId: "audio-file-id"},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != attachment.KindDocument {
		t.Fatalf("attachments = %+v, want one audio document attachment", payload.Attachments)
	}
	if !strings.Contains(payload.Text, caption) || !strings.Contains(payload.Text, "caption: "+caption) {
		t.Fatalf("payload text = %q, want caption and attachment manifest", payload.Text)
	}
}

func TestServerHandleMessage_PublishesTextAndVoiceInOneSessionTurn(t *testing.T) {
	server, turns, locator := newServerMessageHarness(t, 0)
	text := "please summarize this"
	caption := "voice context"
	event := &events.MessageEvent{
		Type: messagetype.Voice,
		Message: &client.Message{
			Chat:    client.Chat{Id: 9001, Type: "private"},
			From:    &client.User{Id: 101},
			Text:    &text,
			Caption: &caption,
			Voice:   &client.Voice{FileId: "voice-file-id"},
		},
	}
	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sessionEnv, ok := findSessionEnvelope(turns.commands, locator.SessionID)
	if !ok {
		t.Fatalf("session command for %q not found in %+v", locator.SessionID, turns.commands)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(sessionEnv.Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != attachment.KindVoice {
		t.Fatalf("attachments = %+v, want one voice attachment", payload.Attachments)
	}
	if !strings.Contains(payload.Text, text) || !strings.Contains(payload.Text, "kind: voice") || !strings.Contains(payload.Text, "caption: voice context") {
		t.Fatalf("payload text = %q, want text and voice manifest", payload.Text)
	}
}

func findSessionEnvelope(commands []actorlayer.Envelope, sessionID string) (actorlayer.Envelope, bool) {
	for _, env := range commands {
		if env.To.Target != baldaexecution.ActorTypeSession {
			continue
		}
		if sessionID != "" && baldaexecution.EnvelopeSessionID(env) != sessionID {
			continue
		}
		return env, true
	}
	return actorlayer.Envelope{}, false
}

func sessionEnvelopesFor(sessionID string, commands []actorlayer.Envelope) []actorlayer.Envelope {
	var out []actorlayer.Envelope
	for _, env := range commands {
		if env.To.Target != baldaexecution.ActorTypeSession {
			continue
		}
		if baldaexecution.EnvelopeSessionID(env) != sessionID {
			continue
		}
		out = append(out, env)
	}
	return out
}

func replyToMessageWithTextFrom(userID int64, isBot bool, text string) *client.Message {
	return &client.Message{
		MessageId: 7,
		From: &client.User{
			Id:    userID,
			IsBot: isBot,
		},
		Text: &text,
	}
}

func replyToMessageFrom(userID int64, isBot bool) *client.Message {
	text := "previous"
	return replyToMessageWithTextFrom(userID, isBot, text)
}
