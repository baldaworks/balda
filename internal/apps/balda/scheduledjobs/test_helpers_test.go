package scheduledjobs

import (
	"context"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
)

const testLocatorTopicSessionID = "tg--1002667079342-8939"

type recordingHandlerCommandBus struct {
	commands    []actorlayer.Envelope
	commandErrs []error
}

func (b *recordingHandlerCommandBus) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if len(b.commandErrs) > 0 {
		err := b.commandErrs[0]
		b.commandErrs = b.commandErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	b.commands = append(b.commands, env)
	return &actortransport.DispatchReceipt{
		Stream:   baldaexecution.DefaultCommandStream,
		Sequence: uint64(len(b.commands)),
		Subject:  baldaexecution.SubjectForEnvelope(env),
		MsgID:    actorlayer.DedupeKeyOrID(env),
	}, nil
}

func (b *recordingHandlerCommandBus) PublishEvent(_ context.Context, _ string, _ actorlayer.Envelope) error {
	return nil
}

type fakeOwnerKVStore struct {
	value any
	ok    bool
	err   error
}

func (s *fakeOwnerKVStore) GetJSON(_ context.Context, _ string) (any, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return s.value, s.ok, nil
}

func (s *fakeOwnerKVStore) SetJSON(_ context.Context, _ string, value any) error {
	if s.err != nil {
		return s.err
	}
	s.value = value
	s.ok = true
	return nil
}
