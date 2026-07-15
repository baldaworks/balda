package slackagent

import (
	"context"
	"fmt"

	baldaslack "github.com/normahq/balda/internal/apps/balda/channel/slack"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

var _ deliverycmd.Adapter = (*Adapter)(nil)

type Adapter struct {
	client           *baldaslack.Client
	logger           zerolog.Logger
	enableStreaming  bool
	suggestedPrompts bool
}

type AdapterConfig struct {
	EnableStreaming  bool
	SuggestedPrompts bool
}

func NewAdapter(client *baldaslack.Client, logger zerolog.Logger, cfg AdapterConfig) *Adapter {
	return &Adapter{
		client:           client,
		logger:           logger.With().Str("component", "balda.channel.slack_agent").Logger(),
		enableStreaming:  cfg.EnableStreaming,
		suggestedPrompts: cfg.SuggestedPrompts,
	}
}

func (a *Adapter) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	var err error
	result := deliverycmd.Result{}
	switch operation.Kind {
	case deliverycmd.OperationPlain:
		_, err = a.send(ctx, locator, operation.Text, false)
	case deliverycmd.OperationMarkdown:
		if deliveryfmt.NormalizeProfile(slackAgentDeliveryProfile(operation.Profile)).Format == deliveryfmt.FormatPlain {
			_, err = a.send(ctx, locator, operation.Text, false)
		} else {
			_, err = a.send(ctx, locator, operation.Text, true)
		}
	case deliverycmd.OperationAgentReply:
		text := operation.Text
		if a.suggestedPrompts {
			text = appendSuggestedPrompts(text)
		}
		result.ProviderMessageID, err = a.send(ctx, locator, text, true)
	case deliverycmd.OperationTyping:
		err = a.sendThinking(ctx, locator)
	case deliverycmd.OperationProgress:
		err = a.sendProgress(ctx, locator, operation.Progress)
	case deliverycmd.OperationDraft:
		err = nil
	default:
		err = fmt.Errorf("unsupported slack agent delivery operation %q", operation.Kind)
	}
	return result, err
}

func (a *Adapter) send(ctx context.Context, locator deliverycmd.Locator, text string, mrkdwn bool) (string, error) {
	if a == nil || a.client == nil {
		return "", fmt.Errorf("slack agent adapter client is required")
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return "", fmt.Errorf("decode slack agent locator: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("unsupported channel type %q for slack agent", locator.ChannelType)
	}
	return a.client.PostMessage(ctx, address.ConversationID, address.ThreadID, text, mrkdwn)
}

func (a *Adapter) sendThinking(_ context.Context, locator deliverycmd.Locator) error {
	a.logger.Debug().
		Str("session_id", locator.SessionID).
		Str("address_key", locator.AddressKey).
		Msg("slack agent thinking/status activity")
	return nil
}

func (a *Adapter) sendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	switch progress.Kind {
	case deliverycmd.ProgressActivity:
		return a.sendThinking(ctx, locator)
	case deliverycmd.ProgressThinking:
		if !a.enableStreaming && progress.Visible {
			return a.sendThinking(ctx, locator)
		}
		if !progress.Visible {
			return a.sendThinking(ctx, locator)
		}
		if progress.Text == "" {
			return a.sendThinking(ctx, locator)
		}
		_, err := a.send(ctx, locator, progress.Text, false)
		return err
	case deliverycmd.ProgressPlanUpdate:
		if !progress.Visible || progress.Text == "" {
			return nil
		}
		_, err := a.send(ctx, locator, progress.Text, false)
		return err
	default:
		return fmt.Errorf("unsupported slack agent progress kind %q", progress.Kind)
	}
}

func appendSuggestedPrompts(text string) string {
	trimmed := text
	if trimmed == "" {
		trimmed = "Done."
	}
	return trimmed + "\n\nTry next:\n- Continue\n- Summarize\n- Suggest next steps"
}

func slackAgentDeliveryProfile(profile deliverycmd.Profile) deliveryfmt.Profile {
	return deliveryfmt.Profile{
		Format:         deliveryfmt.Format(profile.Format),
		TelegramMode:   profile.TelegramMode,
		FormattingMode: profile.FormattingMode,
	}
}
