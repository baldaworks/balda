package natsbus

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

func (b *Bus) Status(ctx context.Context) (swarm.RuntimeStatus, error) {
	status := swarm.RuntimeStatus{
		Transport:     "jetstream",
		Embedded:      b.cfg.NATS.Embedded,
		JetStream:     true,
		ProjectionLag: map[string]uint64{},
	}
	status.CommandsPublishedTotal = b.commandsPublished.Load()
	status.CommandsRunningTotal = b.commandsRunning.Load()
	status.CommandsAckedTotal = b.commandsAcked.Load()
	status.CommandsRetryingTotal = b.commandsRetrying.Load()
	status.CommandsDeadletteredTotal = b.commandsDeadlettered.Load()
	status.CommandDurationSeconds = float64(b.commandDurationNanos.Load()) / float64(time.Second)
	status.ActorDurationSeconds = float64(b.actorDurationNanos.Load()) / float64(time.Second)
	status.DeliveryDuplicateSuppressedTotal = b.duplicateSuppressed.Load()
	if b.conn != nil && !b.conn.IsClosed() {
		status.Running = true
		status.ClientURL = b.conn.ConnectedUrl()
	}
	if b.embedded != nil && b.embedded.URL != "" {
		status.ClientURL = b.embedded.URL
	}
	var err error
	status.Commands, err = b.streamStatus(ctx, b.cfg.Swarm.Commands.Stream)
	if err != nil {
		return status, err
	}
	status.Events, err = b.streamStatus(ctx, b.cfg.Swarm.Events.Stream)
	if err != nil {
		return status, err
	}
	status.DLQ, err = b.streamStatus(ctx, b.cfg.Swarm.DLQ.Stream)
	if err != nil {
		return status, err
	}
	if b.eventConsumer != nil {
		if info, err := b.eventConsumer.Info(ctx); err == nil {
			status.ProjectionLag[swarm.DefaultEventProjectorConsumer] = projectionLag(status.Events.LastSeq, info.AckFloor.Stream)
		}
	}
	status.Worker, err = b.consumerStatus(ctx)
	return status, err
}

func (b *Bus) streamStatus(ctx context.Context, name string) (swarm.StreamStatus, error) {
	stream, err := b.js.Stream(ctx, name)
	if err != nil {
		return swarm.StreamStatus{}, fmt.Errorf("open stream %s: %w", name, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return swarm.StreamStatus{}, fmt.Errorf("read stream %s info: %w", name, err)
	}
	return streamStatusFromInfo(info), nil
}

func streamStatusFromInfo(info *jetstream.StreamInfo) swarm.StreamStatus {
	if info == nil {
		return swarm.StreamStatus{}
	}
	return swarm.StreamStatus{
		Name:     info.Config.Name,
		Messages: info.State.Msgs,
		Bytes:    info.State.Bytes,
		FirstSeq: info.State.FirstSeq,
		LastSeq:  info.State.LastSeq,
	}
}

func projectionLag(lastSeq uint64, ackFloor uint64) uint64 {
	if ackFloor >= lastSeq {
		return 0
	}
	return lastSeq - ackFloor
}

func (b *Bus) consumerStatus(ctx context.Context) (swarm.ConsumerStatus, error) {
	if b.consumer == nil {
		return swarm.ConsumerStatus{}, fmt.Errorf("command consumer is unavailable")
	}
	info, err := b.consumer.Info(ctx)
	if err != nil {
		return swarm.ConsumerStatus{}, fmt.Errorf("read command consumer info: %w", err)
	}
	return swarm.ConsumerStatus{
		Name:           info.Name,
		NumPending:     info.NumPending,
		NumAckPending:  info.NumAckPending,
		NumRedelivered: uint64(info.NumRedelivered),
		NumWaiting:     info.NumWaiting,
		DeliveredSeq:   info.Delivered.Stream,
		AckFloorSeq:    info.AckFloor.Stream,
	}, nil
}
