package swarm

import (
	"context"
	"fmt"
	"strings"
	"time"

	actorengine "github.com/normahq/norma/actorlayer/engine"
)

type runtimeDelivery struct {
	cmd          CommandMessage
	onDeadLetter func(reason string)
}

type runtimeSource struct {
	bus       RuntimeBus
	prepareFn func(context.Context, CommandMessage) (context.Context, func(), actorengine.Delivery)
}

func (s runtimeSource) Run(ctx context.Context, handler actorengine.Handler) error {
	if s.bus == nil {
		return fmt.Errorf("jetstream command bus is required")
	}
	if handler == nil {
		return fmt.Errorf("runtime handler is required")
	}
	if s.prepareFn == nil {
		return fmt.Errorf("runtime command delivery factory is required")
	}
	return s.bus.RunCommandConsumer(ctx, func(ctx context.Context, cmd CommandMessage) error {
		if cmd == nil {
			return fmt.Errorf("command is required")
		}
		executionCtx, stop, delivery := s.prepareFn(ctx, cmd)
		defer stop()
		return handler(executionCtx, delivery)
	})
}

func assertEnvelope(envelope any) (Envelope, error) {
	env, ok := envelope.(Envelope)
	if !ok {
		return Envelope{}, DecodeError(fmt.Errorf("unexpected actor envelope type %T", envelope))
	}
	return env, nil
}

func AssertEnvelope(envelope any) (Envelope, error) {
	return assertEnvelope(envelope)
}

func runtimeAddressOf(envelope any) (string, error) {
	env, err := assertEnvelope(envelope)
	if err != nil {
		return "", err
	}
	to, err := env.To.String()
	if err != nil {
		return "", DecodeError(err)
	}
	if strings.TrimSpace(to) == "" {
		return "", DecodeError(fmt.Errorf("empty actor address"))
	}
	return to, nil
}

func actorLaneKeyFromEnvelope(envelope any) string {
	env, ok := envelope.(Envelope)
	if !ok {
		return "unknown"
	}
	return actorLaneKey(env)
}

func (d *runtimeDelivery) Envelope() any { return d.cmd.Envelope() }
func (d *runtimeDelivery) Attempt() int  { return d.cmd.DeliveryAttempt() }
func (d *runtimeDelivery) MaxAttempts() int {
	return d.cmd.MaxDeliveries()
}
func (d *runtimeDelivery) InProgress(ctx context.Context) error {
	return d.cmd.InProgress(ctx)
}
func (d *runtimeDelivery) Ack(ctx context.Context) error {
	return d.cmd.Ack(ctx)
}
func (d *runtimeDelivery) Retry(ctx context.Context, delay time.Duration, reason string) error {
	return d.cmd.Retry(ctx, delay, reason)
}
func (d *runtimeDelivery) DeadLetter(ctx context.Context, reason string) error {
	if d.onDeadLetter != nil {
		d.onDeadLetter(reason)
	}
	return d.cmd.DeadLetter(ctx, reason)
}
