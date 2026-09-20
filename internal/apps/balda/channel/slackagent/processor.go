package slackagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
)

type ThreadHistoryReader interface {
	ReadThreadBefore(ctx context.Context, channelID, rootTS, beforeTS string) (ThreadSnapshot, error)
}

type inboundProcessor struct {
	chat      chatapp.Handler
	lifecycle SessionLifecycle
	history   ThreadHistoryReader
	files     CurrentFileIngestor
}

func NewInboundProcessor(chat chatapp.Handler, lifecycle SessionLifecycle, history ThreadHistoryReader, files CurrentFileIngestor) InboundProcessor {
	return &inboundProcessor{chat: chat, lifecycle: lifecycle, history: history, files: files}
}

func (p *inboundProcessor) ProcessInbound(ctx context.Context, envelope IngressEnvelope) (turncmd.InboundSettlement, error) {
	if p.lifecycle == nil {
		return retryInbound(), actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	if p.chat == nil {
		return retryInbound(), actorlayer.TransientError(fmt.Errorf("chat handler is unavailable"))
	}
	originalPrompt := envelope.Chat.Text
	if len(envelope.Files) > 0 {
		if p.files == nil {
			return retryInbound(), actorlayer.TransientError(fmt.Errorf("slackagent file ingestor is unavailable"))
		}
		attachments, err := p.files.Ingest(ctx, envelope.Files)
		if err != nil {
			if IsRetryableSlackError(err) {
				return retryInbound(), actorlayer.TransientError(fileIngestError(err))
			}
			return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal, Reason: fileFailureReason(err)}, err
		}
		envelope.Chat.Attachments = attachments
	}
	if err := p.hydrateThreadContext(ctx, &envelope); err != nil {
		return retryInbound(), err
	}
	result, err := p.chat.HandleChat(ctx, envelope.Chat)
	if err != nil {
		return result.Settlement, err
	}
	if result.Activated {
		if err := p.lifecycle.BeginTurn(ctx, envelope.Locator, envelope.InitiatorUserID, originalPrompt); err != nil {
			return retryInbound(), err
		}
	}
	return result.Settlement, nil
}

func (p *inboundProcessor) hydrateThreadContext(ctx context.Context, envelope *IngressEnvelope) error {
	if envelope == nil || envelope.ThreadContext == nil {
		return nil
	}
	if p.history == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent thread history reader is unavailable"))
	}
	request := *envelope.ThreadContext
	snapshot, err := p.history.ReadThreadBefore(ctx, request.ConversationID, request.RootTS, request.BeforeTS)
	if err != nil {
		if IsRetryableSlackError(err) {
			return actorlayer.TransientError(fmt.Errorf("read Slack thread context: %w", err))
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return actorlayer.TransientError(fmt.Errorf("read Slack thread context: %w", err))
		}
		snapshot = UnavailableThreadSnapshot(request, apiErr.Code)
	}
	prompt, err := FormatThreadContext(snapshot, envelope.Chat.Text)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	envelope.Chat.Text = prompt
	return nil
}
