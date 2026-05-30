package swarm

import (
	"context"
	"fmt"
)

type Coordinator struct {
	dispatcher ActorDispatcher
	cfg        Config
}

func NewCoordinator(dispatcher ActorDispatcher, cfg Config) *Coordinator {
	return &Coordinator{dispatcher: dispatcher, cfg: cfg}
}

func (c *Coordinator) Enabled() bool {
	return c != nil && c.cfg.Enabled && c.dispatcher != nil
}

func (c *Coordinator) RuntimeEnabled() bool {
	return c.Enabled()
}

func (c *Coordinator) Dispatch(ctx context.Context, env Envelope) (*DispatchReceipt, error) {
	if c == nil || c.dispatcher == nil {
		return nil, fmt.Errorf("actor dispatcher is required")
	}
	if !c.Enabled() {
		return nil, fmt.Errorf("actor runtime is disabled")
	}
	return c.dispatcher.Dispatch(ctx, env)
}

func (c *Coordinator) PublishEvent(ctx context.Context, subject string, env Envelope) error {
	if c == nil || c.dispatcher == nil {
		return fmt.Errorf("actor event publisher is required")
	}
	publisher, ok := c.dispatcher.(EventPublisher)
	if !ok {
		return fmt.Errorf("actor event publisher is required")
	}
	if !c.Enabled() {
		return fmt.Errorf("actor runtime is disabled")
	}
	return publisher.PublishEvent(ctx, subject, env)
}
