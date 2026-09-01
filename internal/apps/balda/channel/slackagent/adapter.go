package slackagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

var _ deliverycmd.Adapter = (*Adapter)(nil)

type Adapter struct {
	client           MessageClient
	logger           zerolog.Logger
	enableStreaming  bool
	suggestedPrompts bool
	sessionsMu       sync.Mutex
	sessions         map[string]*sessionDeliveryState
}

type MessageClient interface {
	PostMessage(ctx context.Context, channel, threadTS, text string, mrkdwn bool) (string, error)
	SetSessionStatus(ctx context.Context, input SetSessionStatusRequest) error
	RenameSession(ctx context.Context, channelID, threadTS, title string) error
	StartStream(ctx context.Context, channel, threadTS, markdownText string) (string, error)
	AppendStream(ctx context.Context, channel, ts, markdownText string) error
	StopStream(ctx context.Context, channel, ts, markdownText string, status SessionStatus) error
}

type SessionLifecycle interface {
	BeginTurn(ctx context.Context, locator deliverycmd.Locator, initiatorUserID, prompt string) error
	HandleSessionStopped(ctx context.Context, locator deliverycmd.Locator) error
	CloseSession(ctx context.Context, locator deliverycmd.Locator) error
}

type sessionDeliveryState struct {
	mu           sync.Mutex
	streamTS     string
	text         string
	titled       bool
	pendingFinal *pendingFinalStatus
}

type pendingFinalStatus struct {
	text      string
	messageTS string
	status    SessionStatus
}

type AdapterConfig struct {
	EnableStreaming  bool
	SuggestedPrompts bool
}

func NewAdapter(client MessageClient, logger zerolog.Logger, cfg AdapterConfig) *Adapter {
	return &Adapter{
		client:           client,
		logger:           logger.With().Str("component", "balda.channel.slackagent").Logger(),
		enableStreaming:  cfg.EnableStreaming,
		suggestedPrompts: cfg.SuggestedPrompts,
		sessions:         make(map[string]*sessionDeliveryState),
	}
}

func (a *Adapter) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	var err error
	result := deliverycmd.Result{}
	switch operation.Kind {
	case deliverycmd.OperationPlain:
		_, err = a.send(ctx, locator, operation.Text, false)
	case deliverycmd.OperationMarkdown:
		switch {
		case operation.Message != nil:
			_, err = a.sendMessage(ctx, locator, *operation.Message)
		case deliveryfmt.NormalizeDeliveryFormat(operation.DeliveryFormat) == deliveryfmt.DeliveryFormatNone:
			_, err = a.send(ctx, locator, operation.Text, false)
		default:
			_, err = a.send(ctx, locator, operation.Text, true)
		}
	case deliverycmd.OperationAgentReply:
		text := operation.Text
		mrkdwn := true
		if operation.Message != nil {
			text = operation.Message.Text
			switch operation.Message.Name {
			case deliveryfmt.NameSlackMrkdwn:
			case deliveryfmt.NamePlainText:
				mrkdwn = false
			default:
				return deliverycmd.Result{}, fmt.Errorf("unsupported slackagent message format %q", operation.Message.Name)
			}
		}
		if a.suggestedPrompts {
			text = appendSuggestedPrompts(text)
		}
		status := SessionStatusActive
		if operation.Question != nil {
			status = SessionStatusSuspended
		}
		if a.enableStreaming {
			result.ProviderMessageID, err = a.finishStream(ctx, locator, text, status)
		} else {
			result.ProviderMessageID, err = a.deliverFinalMessage(ctx, locator, text, mrkdwn, status)
		}
	case deliverycmd.OperationTyping:
		err = a.sendThinking(ctx, locator)
	case deliverycmd.OperationProgress:
		err = a.sendProgress(ctx, locator, operation.Progress)
	case deliverycmd.OperationDraft:
		err = nil
	default:
		err = fmt.Errorf("unsupported slackagent delivery operation %q", operation.Kind)
	}
	return result, err
}

func (a *Adapter) sendMessage(ctx context.Context, locator deliverycmd.Locator, message deliveryfmt.Message) (string, error) {
	switch message.Name {
	case deliveryfmt.NameSlackMrkdwn:
		return a.send(ctx, locator, message.Text, true)
	case deliveryfmt.NamePlainText:
		return a.send(ctx, locator, message.Text, false)
	default:
		return "", fmt.Errorf("unsupported slackagent message format %q", message.Name)
	}
}

func (a *Adapter) send(ctx context.Context, locator deliverycmd.Locator, text string, mrkdwn bool) (string, error) {
	if a == nil || a.client == nil {
		return "", fmt.Errorf("slackagent adapter client is required")
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return "", fmt.Errorf("decode slackagent locator: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("unsupported channel type %q for slackagent", locator.ChannelType)
	}
	return a.client.PostMessage(ctx, address.ConversationID, address.ThreadID, text, mrkdwn)
}

func (a *Adapter) sendThinking(_ context.Context, locator deliverycmd.Locator) error {
	a.logger.Debug().
		Str("session_id", locator.SessionID).
		Str("address_key", locator.AddressKey).
		Msg("slackagent thinking/status activity")
	return nil
}

func (a *Adapter) sendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	switch progress.Kind {
	case deliverycmd.ProgressActivity:
		return a.sendThinking(ctx, locator)
	case deliverycmd.ProgressThinking:
		if !a.enableStreaming || !progress.Visible {
			return a.sendThinking(ctx, locator)
		}
		if progress.Text == "" {
			return a.sendThinking(ctx, locator)
		}
		return a.streamSnapshot(ctx, locator, progress.Text)
	case deliverycmd.ProgressPlanUpdate:
		if !progress.Visible || progress.Text == "" {
			return nil
		}
		_, err := a.send(ctx, locator, progress.Text, false)
		return err
	default:
		return fmt.Errorf("unsupported slackagent progress kind %q", progress.Kind)
	}
}

func (a *Adapter) BeginTurn(ctx context.Context, locator deliverycmd.Locator, initiatorUserID, prompt string) error {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if pending := state.pendingFinal; pending != nil {
		if err := a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
			ChannelID: address.ConversationID,
			ThreadTS:  address.ThreadID,
			Status:    pending.status,
		}); err != nil {
			return err
		}
		state.pendingFinal = nil
	}
	if err := a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID:       address.ConversationID,
		ThreadTS:        address.ThreadID,
		Status:          SessionStatusProcessing,
		InitiatorUserID: strings.TrimSpace(initiatorUserID),
	}); err != nil {
		return err
	}
	if state.titled {
		return nil
	}
	title := sessionTitle(prompt)
	if err := a.client.RenameSession(ctx, address.ConversationID, address.ThreadID, title); err != nil {
		return err
	}
	state.titled = true
	return nil
}

func (a *Adapter) SetStatus(ctx context.Context, locator deliverycmd.Locator, status SessionStatus) error {
	address, _, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	return a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID: address.ConversationID,
		ThreadTS:  address.ThreadID,
		Status:    status,
	})
}

func (a *Adapter) StopActiveStream(ctx context.Context, locator deliverycmd.Locator, status SessionStatus) error {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.streamTS == "" {
		return a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
			ChannelID: address.ConversationID,
			ThreadTS:  address.ThreadID,
			Status:    status,
		})
	}
	if err := a.client.StopStream(ctx, address.ConversationID, state.streamTS, "", status); err != nil {
		return err
	}
	state.streamTS = ""
	state.text = ""
	return nil
}

func (a *Adapter) HandleSessionStopped(ctx context.Context, locator deliverycmd.Locator) error {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.streamTS = ""
	state.text = ""
	state.pendingFinal = nil
	state.mu.Unlock()
	return a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID: address.ConversationID,
		ThreadTS:  address.ThreadID,
		Status:    SessionStatusActive,
	})
}

func (a *Adapter) CloseSession(ctx context.Context, locator deliverycmd.Locator) error {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID: address.ConversationID,
		ThreadTS:  address.ThreadID,
		Status:    SessionStatusClosed,
	}); err != nil {
		return err
	}
	state.streamTS = ""
	state.text = ""
	state.pendingFinal = nil
	a.sessionsMu.Lock()
	if a.sessions[locator.AddressKey] == state {
		delete(a.sessions, locator.AddressKey)
	}
	a.sessionsMu.Unlock()
	return nil
}

func (a *Adapter) streamSnapshot(ctx context.Context, locator deliverycmd.Locator, snapshot string) error {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.streamTS == "" {
		ts, err := a.client.StartStream(ctx, address.ConversationID, address.ThreadID, snapshot)
		if err != nil {
			return err
		}
		state.streamTS = ts
		state.text = snapshot
		return nil
	}
	if snapshot == state.text {
		return nil
	}
	if !strings.HasPrefix(snapshot, state.text) {
		return fmt.Errorf("slack stream snapshot diverged from previously delivered text")
	}
	delta := strings.TrimPrefix(snapshot, state.text)
	if err := a.client.AppendStream(ctx, address.ConversationID, state.streamTS, delta); err != nil {
		return err
	}
	state.text = snapshot
	return nil
}

func (a *Adapter) finishStream(ctx context.Context, locator deliverycmd.Locator, finalText string, status SessionStatus) (string, error) {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return "", err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.streamTS == "" {
		ts, err := a.client.StartStream(ctx, address.ConversationID, address.ThreadID, finalText)
		if err != nil {
			return "", err
		}
		state.streamTS = ts
		state.text = finalText
	}
	if !strings.HasPrefix(finalText, state.text) {
		return "", fmt.Errorf("slack final response diverged from streamed text")
	}
	streamTS := state.streamTS
	delta := strings.TrimPrefix(finalText, state.text)
	if delta != "" {
		if err := a.client.AppendStream(ctx, address.ConversationID, streamTS, delta); err != nil {
			return "", err
		}
		state.text = finalText
	}
	if err := a.client.StopStream(ctx, address.ConversationID, streamTS, "", status); err != nil {
		return "", err
	}
	state.streamTS = ""
	state.text = ""
	return streamTS, nil
}

func (a *Adapter) deliverFinalMessage(ctx context.Context, locator deliverycmd.Locator, text string, mrkdwn bool, status SessionStatus) (string, error) {
	address, state, err := a.sessionTarget(locator)
	if err != nil {
		return "", err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if pending := state.pendingFinal; pending != nil {
		if pending.text != text || pending.status != status {
			return "", fmt.Errorf("slackagent session has an unresolved final delivery status")
		}
		if err := a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
			ChannelID: address.ConversationID,
			ThreadTS:  address.ThreadID,
			Status:    pending.status,
		}); err != nil {
			return "", err
		}
		messageTS := pending.messageTS
		state.pendingFinal = nil
		return messageTS, nil
	}
	messageTS, err := a.client.PostMessage(ctx, address.ConversationID, address.ThreadID, text, mrkdwn)
	if err != nil {
		return "", err
	}
	if err := a.client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID: address.ConversationID,
		ThreadTS:  address.ThreadID,
		Status:    status,
	}); err != nil {
		state.pendingFinal = &pendingFinalStatus{text: text, messageTS: messageTS, status: status}
		return "", err
	}
	return messageTS, nil
}

func (a *Adapter) sessionTarget(locator deliverycmd.Locator) (LocatorAddress, *sessionDeliveryState, error) {
	if a == nil || a.client == nil {
		return LocatorAddress{}, nil, fmt.Errorf("slackagent adapter client is required")
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return LocatorAddress{}, nil, fmt.Errorf("decode slackagent locator: %w", err)
	}
	if !ok {
		return LocatorAddress{}, nil, fmt.Errorf("unsupported channel type %q for slackagent", locator.ChannelType)
	}
	if strings.TrimSpace(address.ThreadID) == "" {
		return LocatorAddress{}, nil, fmt.Errorf("slackagent thread locator is required")
	}
	key := strings.TrimSpace(locator.AddressKey)
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	state := a.sessions[key]
	if state == nil {
		state = &sessionDeliveryState{}
		a.sessions[key] = state
	}
	return address, state, nil
}

func sessionTitle(prompt string) string {
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" {
		title = "New conversation"
	}
	if utf8.RuneCountInString(title) <= maxSessionTitleRunes {
		return title
	}
	runes := []rune(title)
	return strings.TrimSpace(string(runes[:maxSessionTitleRunes]))
}

func appendSuggestedPrompts(text string) string {
	trimmed := text
	if trimmed == "" {
		trimmed = "Done."
	}
	return trimmed + "\n\nTry next:\n- Continue\n- Summarize\n- Suggest next steps"
}
