package swarm

import "context"

type Coordinator struct {
	mailboxes *MailboxService
}

func NewCoordinator(mailboxes *MailboxService) *Coordinator {
	return &Coordinator{mailboxes: mailboxes}
}

func (c *Coordinator) Enabled() bool {
	return c != nil && c.mailboxes != nil && c.mailboxes.Enabled()
}

func (c *Coordinator) Submit(ctx context.Context, env Envelope) (SubmittedMessage, error) {
	return c.mailboxes.Publish(ctx, env)
}

func (c *Coordinator) CancelSession(ctx context.Context, sessionID string, reason string) (int, error) {
	return c.mailboxes.CancelBySession(ctx, sessionID, reason)
}

func (c *Coordinator) CancelTask(ctx context.Context, taskID string, reason string) (int, error) {
	return c.mailboxes.CancelByTask(ctx, taskID, reason)
}
