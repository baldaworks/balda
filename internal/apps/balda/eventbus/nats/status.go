package natsbus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

type streamSnapshot struct {
	Name     string
	Messages uint64
	Bytes    uint64
	FirstSeq uint64
	LastSeq  uint64
}

func (b *Bus) streamStatus(ctx context.Context, name string) (streamSnapshot, error) {
	stream, err := b.js.Stream(ctx, name)
	if err != nil {
		return streamSnapshot{}, fmt.Errorf("open stream %s: %w", name, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return streamSnapshot{}, fmt.Errorf("read stream %s info: %w", name, err)
	}
	return streamStatusFromInfo(info), nil
}

func streamStatusFromInfo(info *jetstream.StreamInfo) streamSnapshot {
	if info == nil {
		return streamSnapshot{}
	}
	return streamSnapshot{
		Name:     info.Config.Name,
		Messages: info.State.Msgs,
		Bytes:    info.State.Bytes,
		FirstSeq: info.State.FirstSeq,
		LastSeq:  info.State.LastSeq,
	}
}
