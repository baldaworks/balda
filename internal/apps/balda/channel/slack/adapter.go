package slack

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

var _ deliverycmd.Adapter = (*Adapter)(nil)

// Adapter implements channel.ChannelAdapter for the current Slack chat integration.
type Adapter struct {
	client *Client
	logger zerolog.Logger
}

// NewAdapter creates a Slack chat channel adapter.
func NewAdapter(client *Client, logger zerolog.Logger) *Adapter {
	return &Adapter{
		client: client,
		logger: logger.With().Str("component", "balda.channel.slack_chat").Logger(),
	}
}

// Deliver executes one semantic Slack chat delivery operation.
func (a *Adapter) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	var err error
	result := deliverycmd.Result{}
	switch operation.Kind {
	case deliverycmd.OperationPlain:
		err = a.SendPlain(ctx, locator, operation.Text)
	case deliverycmd.OperationMarkdown:
		err = a.SendMarkdownWithProfile(ctx, locator, operation.Profile, operation.Text)
	case deliverycmd.OperationAgentReply:
		result.ProviderMessageID, err = a.SendAgentReplyWithProviderMessageIDAndProfile(ctx, locator, operation.Profile, operation.Text)
	case deliverycmd.OperationDraft:
		err = a.SendDraftPlain(ctx, locator, operation.DraftID, operation.Text)
	case deliverycmd.OperationTyping:
		err = a.SendTyping(ctx, locator)
	case deliverycmd.OperationProgress:
		err = a.SendProgress(ctx, locator, operation.Progress)
	default:
		err = fmt.Errorf("unsupported slack delivery operation %q", operation.Kind)
	}
	return result, err
}

// SendPlain sends a plain text Slack chat message.
func (a *Adapter) SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	_, err := a.send(ctx, locator, text, false)
	return err
}

// SendMarkdown sends a Slack chat mrkdwn message.
func (a *Adapter) SendMarkdown(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return a.SendMarkdownWithProfile(ctx, locator, deliverycmd.Profile{}, text)
}

// SendMarkdownWithProfile sends a Slack chat message using the requested formatting profile.
func (a *Adapter) SendMarkdownWithProfile(ctx context.Context, locator deliverycmd.Locator, profile deliverycmd.Profile, text string) error {
	if deliveryfmt.NormalizeProfile(slackDeliveryProfile(profile)).Format == deliveryfmt.FormatHTML {
		return fmt.Errorf("slack delivery does not support html formatting")
	}
	if deliveryfmt.NormalizeProfile(slackDeliveryProfile(profile)).Format == deliveryfmt.FormatPlain {
		return a.SendPlain(ctx, locator, text)
	}
	_, err := a.send(ctx, locator, text, true)
	return err
}

// SendAgentReply sends agent output to Slack chat.
func (a *Adapter) SendAgentReply(ctx context.Context, locator deliverycmd.Locator, text string) error {
	_, err := a.SendAgentReplyWithProviderMessageID(ctx, locator, text)
	return err
}

// SendAgentReplyWithProviderMessageID sends agent output and returns the Slack chat message timestamp.
func (a *Adapter) SendAgentReplyWithProviderMessageID(ctx context.Context, locator deliverycmd.Locator, text string) (string, error) {
	return a.SendAgentReplyWithProviderMessageIDAndProfile(ctx, locator, deliverycmd.Profile{}, text)
}

// SendAgentReplyWithProviderMessageIDAndProfile sends agent output using Slack chat mrkdwn unless plain is requested.
func (a *Adapter) SendAgentReplyWithProviderMessageIDAndProfile(ctx context.Context, locator deliverycmd.Locator, profile deliverycmd.Profile, text string) (string, error) {
	normalized := deliveryfmt.NormalizeProfile(slackDeliveryProfile(profile))
	if normalized.Format == deliveryfmt.FormatHTML {
		return "", fmt.Errorf("slack delivery does not support html formatting")
	}
	mrkdwn := normalized.Format != deliveryfmt.FormatPlain
	return a.send(ctx, locator, text, mrkdwn)
}

// SendDraftPlain is a no-op for Slack chat v1.
func (a *Adapter) SendDraftPlain(_ context.Context, _ deliverycmd.Locator, _ int, _ string) error {
	return nil
}

// SendTyping is a no-op for Slack chat v1.
func (a *Adapter) SendTyping(_ context.Context, _ deliverycmd.Locator) error {
	return nil
}

// SendProgress renders semantic progress updates for Slack chat.
func (a *Adapter) SendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	if !progress.Visible {
		return nil
	}
	switch progress.Kind {
	case deliverycmd.ProgressThinking:
		return nil
	case deliverycmd.ProgressPlanUpdate:
		return a.SendPlain(ctx, locator, progress.Text)
	default:
		return fmt.Errorf("unsupported slack progress kind %q", progress.Kind)
	}
}

func (a *Adapter) send(ctx context.Context, locator deliverycmd.Locator, text string, mrkdwn bool) (string, error) {
	if a == nil || a.client == nil {
		return "", fmt.Errorf("slack adapter client is required")
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return "", fmt.Errorf("decode slack locator: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("unsupported channel type %q for slack", locator.ChannelType)
	}
	threadTS := ""
	if address.Type == addressTypeThread {
		threadTS = address.ThreadTS
	}
	return a.client.PostMessage(ctx, address.Channel, threadTS, text, mrkdwn)
}

func slackDeliveryProfile(profile deliverycmd.Profile) deliveryfmt.Profile {
	return deliveryfmt.Profile{
		Format:         deliveryfmt.Format(profile.Format),
		TelegramMode:   profile.TelegramMode,
		FormattingMode: profile.FormattingMode,
	}
}
