package actors

import (
	"context"
	"net/http"
	"reflect"
	"unsafe"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"testing"
)

const testTelegramUserID101 = "tg-101"

type cancelSessionCall struct {
	SessionID   string
	ClearQueued bool
}

type fakeTurnDispatcher struct {
	commands          []swarm.Envelope
	cancelCalls       []cancelSessionCall
	enqueueCalls      []TurnTask
	cancelHadInFlight bool
	cancelDropped     int
	cancelErr         error
}

func (f *fakeTurnDispatcher) Enqueue(task TurnTask) (int, error) {
	f.enqueueCalls = append(f.enqueueCalls, task)
	return 0, nil
}

func (f *fakeTurnDispatcher) PublishCommand(_ context.Context, env swarm.Envelope) (*swarm.CommandPublishResult, error) {
	f.commands = append(f.commands, env)
	return &swarm.CommandPublishResult{
		Stream:   swarm.DefaultCommandStream,
		Sequence: uint64(len(f.commands)),
		Subject:  swarm.SubjectForEnvelope(env),
		MsgID:    swarm.DedupeKeyOrID(env),
	}, nil
}

func (*fakeTurnDispatcher) PublishEvent(context.Context, string, swarm.Envelope) error { return nil }

func (f *fakeTurnDispatcher) CancelSession(locator session.SessionLocator, clearQueued bool) (bool, int, error) {
	f.cancelCalls = append(f.cancelCalls, cancelSessionCall{
		SessionID:   locator.SessionID,
		ClearQueued: clearQueued,
	})
	if f.cancelErr != nil {
		return false, 0, f.cancelErr
	}
	return f.cancelHadInFlight, f.cancelDropped, nil
}

type recordingHandlerCommandBus struct {
	commands      []swarm.Envelope
	commandErrs   []error
	eventSubjects []string
	eventEnvs     []swarm.Envelope
	eventErrs     []error
}

func (b *recordingHandlerCommandBus) PublishCommand(_ context.Context, env swarm.Envelope) (*swarm.CommandPublishResult, error) {
	if len(b.commandErrs) > 0 {
		err := b.commandErrs[0]
		b.commandErrs = b.commandErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	b.commands = append(b.commands, env)
	return &swarm.CommandPublishResult{Stream: swarm.DefaultCommandStream, Sequence: uint64(len(b.commands)), Subject: swarm.SubjectForEnvelope(env), MsgID: swarm.DedupeKeyOrID(env)}, nil
}

func (b *recordingHandlerCommandBus) PublishEvent(_ context.Context, subject string, env swarm.Envelope) error {
	b.eventSubjects = append(b.eventSubjects, subject)
	b.eventEnvs = append(b.eventEnvs, env)
	if len(b.eventErrs) > 0 {
		err := b.eventErrs[0]
		b.eventErrs = b.eventErrs[1:]
		return err
	}
	return nil
}

type fakeTelegramClient struct {
	client.ClientWithResponsesInterface
	sendErr  error
	messages []client.SendMessageJSONRequestBody
}

func (c *fakeTelegramClient) SendMessageWithResponse(_ context.Context, body client.SendMessageJSONRequestBody, _ ...client.RequestEditorFn) (*client.SendMessageResponse, error) {
	c.messages = append(c.messages, body)
	if c.sendErr != nil {
		return nil, c.sendErr
	}
	return &client.SendMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendMessage200Ok `json:"ok"`
			Result client.Message          `json:"result"`
		}{
			Ok:     true,
			Result: client.Message{MessageId: len(c.messages)},
		},
	}, nil
}

func newBaldaTestTelegramAdapter() *baldatelegram.Adapter {
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	return baldatelegram.NewAdapter(baldatelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
}

func newBaldaSessionManagerWithSession(t *testing.T, locator session.SessionLocator, ts *session.TopicSession) *session.Manager {
	t.Helper()

	m := &session.Manager{}
	setUnexportedField(t, m, "sessions", map[string]*session.TopicSession{locator.SessionID: ts})
	setUnexportedField(t, m, "sessionStore", &fakeBaldaRestoreSessionStore{})
	return m
}

func newBaldaTopicSession(t *testing.T, sessionID string) *session.TopicSession {
	t.Helper()

	ts := &session.TopicSession{}
	setUnexportedField(t, ts, "sessionID", sessionID)
	return ts
}

type fakeBaldaRestoreSessionStore struct {
	record         baldastate.SessionRecord
	foundByAddress bool
	lastUpsert     baldastate.SessionRecord
}

func (f *fakeBaldaRestoreSessionStore) Upsert(_ context.Context, record baldastate.SessionRecord) error {
	f.lastUpsert = record
	f.record = record
	f.foundByAddress = true
	return nil
}

func (f *fakeBaldaRestoreSessionStore) GetByAddress(_ context.Context, channelType, addressKey string) (baldastate.SessionRecord, bool, error) {
	if !f.foundByAddress {
		return baldastate.SessionRecord{}, false, nil
	}
	if f.record.ChannelType != channelType || f.record.AddressKey != addressKey {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (f *fakeBaldaRestoreSessionStore) GetBySessionID(_ context.Context, sessionID string) (baldastate.SessionRecord, bool, error) {
	if !f.foundByAddress || f.record.SessionID != sessionID {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (*fakeBaldaRestoreSessionStore) DeleteBySessionID(context.Context, string) error {
	return nil
}

func (f *fakeBaldaRestoreSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	if !f.foundByAddress {
		return nil, nil
	}
	return []baldastate.SessionRecord{f.record}, nil
}

func setUnexportedField[T any](t *testing.T, target any, fieldName string, value T) {
	t.Helper()

	rv := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
