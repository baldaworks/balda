package handlers

import (
	"context"
	"iter"
	"net/http"
	"strings"
	"testing"
	"time"

	relaychannel "github.com/normahq/relay/internal/apps/relay/channel"
	relaytelegram "github.com/normahq/relay/internal/apps/relay/channel/telegram"
	"github.com/normahq/relay/internal/apps/relay/messenger"
	relaysession "github.com/normahq/relay/internal/apps/relay/session"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	relayRunTurnGenericEmptyTerminalMessage = "The provider ended the turn without a usable reply. Please try again."
	relayRunTurnFinalAnswerText             = "final answer"
	relayRunTurnThinkingOne                 = "Thinking."
	relayRunTurnThinkingTwo                 = "Thinking.."
)

type relayRunTurnTelegramClient struct {
	client.ClientWithResponsesInterface
	drafts      []client.SendMessageDraftJSONRequestBody
	messages    []client.SendMessageJSONRequestBody
	chatActions []client.SendChatActionJSONRequestBody
}

func (c *relayRunTurnTelegramClient) SendMessageWithResponse(
	_ context.Context,
	body client.SendMessageJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendMessageResponse, error) {
	c.messages = append(c.messages, body)
	return &client.SendMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendMessage200Ok `json:"ok"`
			Result client.Message          `json:"result"`
		}{
			Ok:     true,
			Result: client.Message{MessageId: len(c.messages)},
		},
	}, nil
}

func (c *relayRunTurnTelegramClient) SendMessageDraftWithResponse(
	_ context.Context,
	body client.SendMessageDraftJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendMessageDraftResponse, error) {
	c.drafts = append(c.drafts, body)
	return &client.SendMessageDraftResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendMessageDraft200Ok `json:"ok"`
			Result bool                         `json:"result"`
		}{
			Ok:     true,
			Result: true,
		},
	}, nil
}

func (c *relayRunTurnTelegramClient) SendChatActionWithResponse(
	_ context.Context,
	body client.SendChatActionJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendChatActionResponse, error) {
	c.chatActions = append(c.chatActions, body)
	return &client.SendChatActionResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendChatAction200Ok `json:"ok"`
			Result bool                       `json:"result"`
		}{
			Ok:     true,
			Result: true,
		},
	}, nil
}

func relayRunTurnDraftText(t *testing.T, draft client.SendMessageDraftJSONRequestBody) string {
	t.Helper()
	if draft.Text == nil {
		t.Fatal("draft text is nil")
	}
	return *draft.Text
}

func newRelayPlanSnapshot(entries ...map[string]any) map[string]any {
	return map[string]any{
		"entries": entries,
	}
}

func TestRunTurn_SendsPlanUpdateDraftFromCustomMetadataInDM(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, false)
	h.planUpdatesEnabled = true

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		plan := adksession.NewEvent(invocationID)
		plan.Partial = true
		plan.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan": newRelayPlanSnapshot(
				map[string]any{"content": "Run tests", "status": "in_progress", "priority": "medium"},
				map[string]any{"content": "Ship fix", "status": "pending", "priority": "high"},
			),
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{plan, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true, Thinking: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 1 {
		t.Fatalf("draft calls = %d, want 1", len(tgClient.drafts))
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[0]); got != "Plan update\n- [in progress] Run tests\n- [pending] Ship fix" {
		t.Fatalf("draft[0].text = %q, want plan update text", got)
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnFinalAnswerText {
		t.Fatalf("message text = %q, want final answer", got)
	}
}

func TestRunTurn_SendsProgressForNonTerminalEventsInDM(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true, Thinking: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 1 {
		t.Fatalf("draft calls = %d, want 1", len(tgClient.drafts))
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[0]); got != relayRunTurnThinkingOne {
		t.Fatalf("draft[0].text = %q, want %s", got, relayRunTurnThinkingOne)
	}
	for i, draft := range tgClient.drafts {
		if draft.MessageThreadId == nil || *draft.MessageThreadId != 77 {
			t.Fatalf("draft[%d].message_thread_id = %v, want 77", i, draft.MessageThreadId)
		}
	}

	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chat action calls = %d, want 1", len(tgClient.chatActions))
	}
	for i, action := range tgClient.chatActions {
		if action.Action != "typing" {
			t.Fatalf("chatActions[%d].action = %q, want typing", i, action.Action)
		}
		if action.ChatId != 9001 {
			t.Fatalf("chatActions[%d].chat_id = %d, want 9001", i, action.ChatId)
		}
		if action.MessageThreadId == nil || *action.MessageThreadId != 77 {
			t.Fatalf("chatActions[%d].message_thread_id = %v, want 77", i, action.MessageThreadId)
		}
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if !strings.Contains(tgClient.messages[0].Text, relayRunTurnFinalAnswerText) {
		t.Fatalf("message text = %q, want to contain final answer", tgClient.messages[0].Text)
	}
	if tgClient.messages[0].ParseMode == nil || *tgClient.messages[0].ParseMode != "MarkdownV2" {
		t.Fatalf("parse_mode = %v, want MarkdownV2", tgClient.messages[0].ParseMode)
	}
}

func TestRunTurn_SkipsTypingAndDraftWhenAllProgressDisabled(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 0 {
		t.Fatalf("draft calls = %d, want 0", len(tgClient.drafts))
	}
	if len(tgClient.chatActions) != 0 {
		t.Fatalf("chat action calls = %d, want 0", len(tgClient.chatActions))
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if !strings.Contains(tgClient.messages[0].Text, relayRunTurnFinalAnswerText) {
		t.Fatalf("message text = %q, want to contain final answer", tgClient.messages[0].Text)
	}
}

func TestRunTurn_SendsTypingWithoutThinkingDraftInPublicChat(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true, Thinking: false}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 0 {
		t.Fatalf("draft calls = %d, want 0", len(tgClient.drafts))
	}
	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chat action calls = %d, want 1", len(tgClient.chatActions))
	}
	for i, action := range tgClient.chatActions {
		if action.Action != "typing" {
			t.Fatalf("chatActions[%d].action = %q, want typing", i, action.Action)
		}
	}
}

func TestRunTurn_SendsPlanUpdateMessagesInPublicChat(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, true)
	h.planUpdatesEnabled = true

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		planOne := adksession.NewEvent(invocationID)
		planOne.Partial = true
		planOne.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan":        newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "pending"}),
		}

		planTwo := adksession.NewEvent(invocationID)
		planTwo.Partial = true
		planTwo.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan": newRelayPlanSnapshot(
				map[string]any{"content": "Run tests", "status": "completed"},
				map[string]any{"content": "Ship fix", "status": "in_progress"},
			),
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{planOne, planTwo, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 0 {
		t.Fatalf("draft calls = %d, want 0", len(tgClient.drafts))
	}
	if len(tgClient.messages) != 3 {
		t.Fatalf("message calls = %d, want 3", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != "Plan update\n- [pending] Run tests" {
		t.Fatalf("messages[0].text = %q, want first plan update", got)
	}
	if got := tgClient.messages[1].Text; got != "Plan update\n- [completed] Run tests\n- [in progress] Ship fix" {
		t.Fatalf("messages[1].text = %q, want second plan update", got)
	}
	if got := tgClient.messages[2].Text; got != relayRunTurnFinalAnswerText {
		t.Fatalf("messages[2].text = %q, want final answer", got)
	}
	if tgClient.messages[0].ParseMode != nil || tgClient.messages[1].ParseMode != nil {
		t.Fatal("plan update messages must be plain text without parse_mode")
	}
}

func TestRunTurn_SendsProgressAndGenericMessageForNonThoughtEventsWithoutFinalReply(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		toolCall := adksession.NewEvent(invocationID)
		toolCall.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "tool.one"}},
			},
		}

		partial := adksession.NewEvent(invocationID)
		partial.Partial = true
		partial.Content = genai.NewContentFromText("visible", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{toolCall, partial, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chat action calls = %d, want 1", len(tgClient.chatActions))
	}
	if len(tgClient.drafts) != 0 {
		t.Fatalf("draft calls = %d, want 0", len(tgClient.drafts))
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnGenericEmptyTerminalMessage {
		t.Fatalf("message text = %q, want %q", got, relayRunTurnGenericEmptyTerminalMessage)
	}
}

func TestRunTurn_SendsTypingAgainAfterThrottleInterval(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	baseTime := time.Date(2026, 4, 24, 20, 0, 0, 0, time.UTC)
	times := []time.Time{
		baseTime,
		baseTime.Add(telegramProgressThrottleInterval - time.Second),
		baseTime.Add(telegramProgressThrottleInterval),
	}
	timeIdx := 0
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
		now: func() time.Time {
			if timeIdx >= len(times) {
				return times[len(times)-1]
			}
			now := times[timeIdx]
			timeIdx++
			return now
		},
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.chatActions) != 2 {
		t.Fatalf("chat action calls = %d, want 2", len(tgClient.chatActions))
	}
	if timeIdx != len(times) {
		t.Fatalf("clock calls = %d, want %d", timeIdx, len(times))
	}
}

func TestRunTurn_SendsThinkingDraftAgainAfterThrottleInterval(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	baseTime := time.Date(2026, 4, 24, 20, 0, 0, 0, time.UTC)
	times := []time.Time{
		baseTime,
		baseTime.Add(telegramProgressThrottleInterval - time.Second),
		baseTime.Add(telegramProgressThrottleInterval),
	}
	timeIdx := 0
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
		now: func() time.Time {
			if timeIdx >= len(times) {
				return times[len(times)-1]
			}
			now := times[timeIdx]
			timeIdx++
			return now
		},
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Thinking: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 2 {
		t.Fatalf("draft calls = %d, want 2", len(tgClient.drafts))
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[0]); got != relayRunTurnThinkingOne {
		t.Fatalf("draft[0].text = %q, want %s", got, relayRunTurnThinkingOne)
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[1]); got != relayRunTurnThinkingTwo {
		t.Fatalf("draft[1].text = %q, want %s", got, relayRunTurnThinkingTwo)
	}
	if timeIdx != len(times) {
		t.Fatalf("clock calls = %d, want %d", timeIdx, len(times))
	}
}

func TestRunTurn_DoesNotFallBackToThinkingAfterPlanDraftInDM(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	baseTime := time.Date(2026, 4, 24, 20, 0, 0, 0, time.UTC)
	times := []time.Time{
		baseTime,
		baseTime.Add(telegramProgressThrottleInterval),
		baseTime.Add(2 * telegramProgressThrottleInterval),
	}
	timeIdx := 0
	h := &RelayHandler{
		channel:            channel,
		logger:             zerolog.Nop(),
		planUpdatesEnabled: true,
		now: func() time.Time {
			if timeIdx >= len(times) {
				return times[len(times)-1]
			}
			now := times[timeIdx]
			timeIdx++
			return now
		},
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		thought := adksession.NewEvent(invocationID)
		thought.Partial = true
		thought.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Thought: true}},
		}

		plan := adksession.NewEvent(invocationID)
		plan.Partial = true
		plan.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan":        newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "in_progress"}),
		}

		thoughtTwo := adksession.NewEvent(invocationID)
		thoughtTwo.Partial = true
		thoughtTwo.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Thought: true}},
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{thought, plan, thoughtTwo, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Thinking: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 2 {
		t.Fatalf("draft calls = %d, want 2", len(tgClient.drafts))
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[0]); got != relayRunTurnThinkingOne {
		t.Fatalf("draft[0].text = %q, want %s", got, relayRunTurnThinkingOne)
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[1]); got != "Plan update\n- [in progress] Run tests" {
		t.Fatalf("draft[1].text = %q, want plan update text", got)
	}
}

func TestRunTurn_SendsFinalResponseWithoutParseModeWhenConfiguredNone(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunner(t)
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if tgClient.messages[0].ParseMode != nil {
		t.Fatalf("parse_mode = %v, want nil", *tgClient.messages[0].ParseMode)
	}
}

func TestRunTurn_SkipsExactDuplicateFinalAfterStreamedText(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		partial := adksession.NewEvent(invocationID)
		partial.Partial = true
		partial.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)

		final := adksession.NewEvent(invocationID)
		final.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{partial, final, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := strings.TrimSpace(tgClient.messages[0].Text); got != relayRunTurnFinalAnswerText {
		t.Fatalf("message text = %q, want final answer", tgClient.messages[0].Text)
	}
}

func TestRunTurn_MergesFinalResponseDeltaChunksOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		chunkOne := adksession.NewEvent(invocationID)
		chunkOne.Content = genai.NewContentFromText("Пункт списка1\n", genai.RoleModel)

		chunkTwo := adksession.NewEvent(invocationID)
		chunkTwo.Content = genai.NewContentFromText("- Пункт списка2\n", genai.RoleModel)

		chunkThree := adksession.NewEvent(invocationID)
		chunkThree.Content = genai.NewContentFromText("- Пункт списка3", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{chunkOne, chunkTwo, chunkThree, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	want := "Пункт списка1\n- Пункт списка2\n- Пункт списка3"
	if got := tgClient.messages[0].Text; got != want {
		t.Fatalf("message text = %q, want %q", got, want)
	}
}

func TestRunTurn_AppendsFinalResponseTextEventsOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		chunkOne := adksession.NewEvent(invocationID)
		chunkOne.Content = genai.NewContentFromText("Doing", genai.RoleModel)

		chunkTwo := adksession.NewEvent(invocationID)
		chunkTwo.Content = genai.NewContentFromText("Doing well", genai.RoleModel)

		chunkThree := adksession.NewEvent(invocationID)
		chunkThree.Content = genai.NewContentFromText("Doing well.", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{chunkOne, chunkTwo, chunkThree, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != "DoingDoing wellDoing well." {
		t.Fatalf("message text = %q, want appended chunks", got)
	}
}

func TestRunTurn_SendsGenericMessageWhenOnlyNonFinalTextExistsOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		nonFinalOne := adksession.NewEvent(invocationID)
		nonFinalOne.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "tool.one"}},
				{Text: "old fallback"},
			},
		}

		nonFinalTwo := adksession.NewEvent(invocationID)
		nonFinalTwo.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "tool.two"}},
				{Text: "new fallback"},
			},
		}

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{nonFinalOne, nonFinalTwo, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnGenericEmptyTerminalMessage {
		t.Fatalf("message text = %q, want %q", got, relayRunTurnGenericEmptyTerminalMessage)
	}
}

func TestRunTurn_DoesNotLeakNonFinalProgressTextInPublicChat(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		progressOne := adksession.NewEvent(invocationID)
		progressOne.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "approve"}},
				{Text: "Сделаю: поставлю Approve и добавлю комментарий."},
			},
		}

		progressTwo := adksession.NewEvent(invocationID)
		progressTwo.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "comment"}},
				{Text: "Ставлю Approve и добавляю общий комментарий."},
			},
		}

		final := adksession.NewEvent(invocationID)
		final.Content = genai.NewContentFromText("Готово.\n\n- В PR 1762 поставил Approved.\n- Добавил комментарий.", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{progressOne, progressTwo, final, done}
	})
	locator := relaysession.NewTelegramSessionLocator(-5173524191, 0)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "approve PR", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chat action calls = %d, want 1", len(tgClient.chatActions))
	}
	if len(tgClient.drafts) != 0 {
		t.Fatalf("draft calls = %d, want 0", len(tgClient.drafts))
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	got := tgClient.messages[0].Text
	if strings.Contains(got, "Сделаю") || strings.Contains(got, "Ставлю Approve") {
		t.Fatalf("message text = %q, contains non-final progress text", got)
	}
	if !strings.Contains(got, "Готово.") || !strings.Contains(got, "Approved") {
		t.Fatalf("message text = %q, want final response", got)
	}
}

func TestRunTurn_SendsFinalTextFromTurnCompleteEvent(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		progress := adksession.NewEvent(invocationID)
		progress.Partial = true
		progress.Content = genai.NewContentFromText("working...", genai.RoleModel)

		toolUpdate := adksession.NewEvent(invocationID)
		toolUpdate.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						ID:   "tool-1",
						Name: "acp_tool_call_update",
						Response: map[string]any{
							"status": "completed",
						},
					},
				},
			},
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.FinishReason = genai.FinishReasonStop
		done.TurnComplete = true

		return []*adksession.Event{progress, toolUpdate, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnFinalAnswerText {
		t.Fatalf("message text = %q, want final answer", got)
	}
}

func TestRunTurn_UsesLegacyPlanStateDeltaFallback(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, true)
	h.planUpdatesEnabled = true

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		plan := adksession.NewEvent(invocationID)
		plan.Partial = true
		plan.Actions.StateDelta["acp_plan"] = newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "pending"})

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{plan, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 2 {
		t.Fatalf("message calls = %d, want 2", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != "Plan update\n- [pending] Run tests" {
		t.Fatalf("messages[0].text = %q, want legacy plan update", got)
	}
}

func TestRunTurn_DeduplicatesRepeatedPlanUpdates(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, true)
	h.planUpdatesEnabled = true

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		planOne := adksession.NewEvent(invocationID)
		planOne.Partial = true
		planOne.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan":        newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "pending"}),
		}

		planTwo := adksession.NewEvent(invocationID)
		planTwo.Partial = true
		planTwo.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan":        newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "pending"}),
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{planOne, planTwo, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Typing: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 2 {
		t.Fatalf("message calls = %d, want 2", len(tgClient.messages))
	}
}

func TestRunTurn_PlanUpdatesDisabledKeepsLegacyThinkingBehavior(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, true)
	h.planUpdatesEnabled = false

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		plan := adksession.NewEvent(invocationID)
		plan.Partial = true
		plan.CustomMetadata = map[string]any{
			"acp_update_kind": "plan",
			"acp_plan":        newRelayPlanSnapshot(map[string]any{"content": "Run tests", "status": "pending"}),
		}

		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.TurnComplete = true

		return []*adksession.Event{plan, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	progressPolicy := relaychannel.ProgressPolicy{Thinking: true}
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, progressPolicy); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.drafts) != 1 {
		t.Fatalf("draft calls = %d, want 1", len(tgClient.drafts))
	}
	if got := relayRunTurnDraftText(t, tgClient.drafts[0]); got != relayRunTurnThinkingOne {
		t.Fatalf("draft[0].text = %q, want %s", got, relayRunTurnThinkingOne)
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
}

func TestRunTurn_DoesNotSendWithoutTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		final := adksession.NewEvent(invocationID)
		final.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		return []*adksession.Event{final}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 0 {
		t.Fatalf("message calls = %d, want 0", len(tgClient.messages))
	}
}

func TestRunTurn_SendsGenericMessageWhenOnlyPartialTextExistsOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		partialOne := adksession.NewEvent(invocationID)
		partialOne.Partial = true
		partialOne.Content = genai.NewContentFromText("Doing", genai.RoleModel)

		partialTwo := adksession.NewEvent(invocationID)
		partialTwo.Partial = true
		partialTwo.Content = genai.NewContentFromText(" well", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{partialOne, partialTwo, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnGenericEmptyTerminalMessage {
		t.Fatalf("message text = %q, want %q", got, relayRunTurnGenericEmptyTerminalMessage)
	}
}

func TestRunTurn_SendsGenericMessageWhenOnlyPartialMarkdownChunksExistOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		partialOne := adksession.NewEvent(invocationID)
		partialOne.Partial = true
		partialOne.Content = genai.NewContentFromText("**Статус задачи**", genai.RoleModel)

		partialTwo := adksession.NewEvent(invocationID)
		partialTwo.Partial = true
		partialTwo.Content = genai.NewContentFromText("\n", genai.RoleModel)

		partialThree := adksession.NewEvent(invocationID)
		partialThree.Partial = true
		partialThree.Content = genai.NewContentFromText("- **Task:** `relay-runtime`\n- **Status:** in progress", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{partialOne, partialTwo, partialThree, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnGenericEmptyTerminalMessage {
		t.Fatalf("message text = %q, want %q", got, relayRunTurnGenericEmptyTerminalMessage)
	}
}

func TestRunTurn_SendsGenericMessageWhenOnlyThoughtOrPartialTextExistsOnTurnComplete(t *testing.T) {
	t.Parallel()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
	h := &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		thought := adksession.NewEvent(invocationID)
		thought.Partial = true
		thought.Content = &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Thought: true, Text: "secret"},
			},
		}

		partial := adksession.NewEvent(invocationID)
		partial.Partial = true
		partial.Content = genai.NewContentFromText("visible", genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{thought, partial, done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnGenericEmptyTerminalMessage {
		t.Fatalf("message text = %q, want %q", got, relayRunTurnGenericEmptyTerminalMessage)
	}
}

func TestRunTurn_SendsFinishReasonMessageOnEmptyTurnComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		finishReason genai.FinishReason
		want         string
	}{
		{name: "empty", finishReason: genai.FinishReason(""), want: relayRunTurnGenericEmptyTerminalMessage},
		{name: "unspecified", finishReason: genai.FinishReasonUnspecified, want: relayRunTurnGenericEmptyTerminalMessage},
		{name: "stop", finishReason: genai.FinishReasonStop, want: relayRunTurnGenericEmptyTerminalMessage},
		{name: "max tokens", finishReason: genai.FinishReasonMaxTokens, want: "The provider hit the output limit before producing a visible reply. Ask for a shorter answer or split the request."},
		{name: "safety", finishReason: genai.FinishReasonSafety, want: "The provider blocked this turn for safety reasons. Please rephrase and try again."},
		{name: "recitation", finishReason: genai.FinishReasonRecitation, want: "The provider blocked this turn because it may reproduce protected source material. Please rephrase and try again."},
		{name: "language", finishReason: genai.FinishReasonLanguage, want: "The provider could not answer because the request used an unsupported language. Please rephrase in a supported language and try again."},
		{name: "other", finishReason: genai.FinishReasonOther, want: relayRunTurnGenericEmptyTerminalMessage},
		{name: "blocklist", finishReason: genai.FinishReasonBlocklist, want: "The provider blocked this turn because it matched restricted terms. Please rephrase and try again."},
		{name: "prohibited content", finishReason: genai.FinishReasonProhibitedContent, want: "The provider rejected this turn as prohibited content. Please rephrase and try again."},
		{name: "spii", finishReason: genai.FinishReasonSPII, want: "The provider blocked this turn because it may contain sensitive personal information. Please remove that information and try again."},
		{name: "malformed function call", finishReason: genai.FinishReasonMalformedFunctionCall, want: "The provider ended the turn with an invalid function call. Please try again."},
		{name: "unexpected tool call", finishReason: genai.FinishReasonUnexpectedToolCall, want: "The provider ended the turn with an unexpected tool call. Please try again."},
		{name: "image safety", finishReason: genai.FinishReasonImageSafety, want: "The provider blocked image generation for safety reasons. Please try a different request."},
		{name: "image prohibited content", finishReason: genai.FinishReasonImageProhibitedContent, want: "The provider rejected image generation as prohibited content. Please try a different request."},
		{name: "no image", finishReason: genai.FinishReasonNoImage, want: "The provider completed the turn without returning an image. Please try a different request."},
		{name: "image recitation", finishReason: genai.FinishReasonImageRecitation, want: "The provider blocked image generation because it may reproduce protected source material. Please try a different request."},
		{name: "image other", finishReason: genai.FinishReasonImageOther, want: "The provider ended image generation without a usable result. Please try again."},
		{name: "unknown", finishReason: genai.FinishReason("MYSTERY"), want: relayRunTurnGenericEmptyTerminalMessage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, tgClient := newRelayRunTurnTestHandler(t, false)
			adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
				done := adksession.NewEvent(invocationID)
				done.FinishReason = tt.finishReason
				done.TurnComplete = true

				return []*adksession.Event{done}
			})
			locator := relaysession.NewTelegramSessionLocator(9001, 77)
			if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
				t.Fatalf("runTurn() error = %v", err)
			}

			if len(tgClient.messages) != 1 {
				t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
			}
			if got := tgClient.messages[0].Text; got != tt.want {
				t.Fatalf("message text = %q, want %q", got, tt.want)
			}
			if tgClient.messages[0].ParseMode != nil {
				t.Fatalf("parse_mode = %v, want nil", *tgClient.messages[0].ParseMode)
			}
		})
	}
}

func TestRunTurn_AppendsProviderMessageExcerptForEmptyTurnComplete(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, false)
	rawMessage := "line   one\nline\t two   " + strings.Repeat("x", 400)
	expectedExcerpt := "line one line two " + strings.Repeat("x", 282)

	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		done := adksession.NewEvent(invocationID)
		done.FinishReason = genai.FinishReasonProhibitedContent
		done.ErrorMessage = rawMessage
		done.TurnComplete = true

		return []*adksession.Event{done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	want := "The provider rejected this turn as prohibited content. Please rephrase and try again.\n\nProvider message: " + expectedExcerpt
	if got := tgClient.messages[0].Text; got != want {
		t.Fatalf("message text = %q, want %q", got, want)
	}
}

func TestRunTurn_DoesNotAppendFinishReasonMessageWhenFinalTextExists(t *testing.T) {
	t.Parallel()

	h, tgClient := newRelayRunTurnTestHandler(t, true)
	adkRunner, sessionID := newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)
		done.FinishReason = genai.FinishReasonMaxTokens
		done.TurnComplete = true

		return []*adksession.Event{done}
	})
	locator := relaysession.NewTelegramSessionLocator(9001, 77)
	if err := h.runTurn(context.Background(), "hello", adkRunner, "tg-101", sessionID, sessionID, locator, 41, relaychannel.ProgressPolicy{}); err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	if got := tgClient.messages[0].Text; got != relayRunTurnFinalAnswerText {
		t.Fatalf("message text = %q, want final answer", got)
	}
}

func newRelayRunTurnTestRunner(t *testing.T) (*runner.Runner, string) {
	t.Helper()

	return newRelayRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		thoughtOne := adksession.NewEvent(invocationID)
		thoughtOne.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Thought: true}},
		}

		thoughtTwo := adksession.NewEvent(invocationID)
		thoughtTwo.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Thought: true}},
		}

		reply := adksession.NewEvent(invocationID)
		reply.Content = genai.NewContentFromText(relayRunTurnFinalAnswerText, genai.RoleModel)

		done := adksession.NewEvent(invocationID)
		done.TurnComplete = true

		return []*adksession.Event{thoughtOne, thoughtTwo, reply, done}
	})
}

func newRelayRunTurnTestHandler(t *testing.T, agentReplyFormattingNone bool) (*RelayHandler, *relayRunTurnTelegramClient) {
	t.Helper()

	tgClient := &relayRunTurnTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	if agentReplyFormattingNone {
		msg.SetAgentReplyFormattingMode("none")
	}
	channel := relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})

	return &RelayHandler{
		channel: channel,
		logger:  zerolog.Nop(),
	}, tgClient
}

func newRelayRunTurnTestRunnerWithEvents(
	t *testing.T,
	eventsFn func(invocationID string) []*adksession.Event,
) (*runner.Runner, string) {
	t.Helper()

	relayAgent, err := adkagent.New(adkagent.Config{
		Name:        "RelayRunTurnTestAgent",
		Description: "Emits scripted events for relay runTurn tests",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				for _, ev := range eventsFn(ctx.InvocationID()) {
					if !yield(ev, nil) {
						return
					}
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	adkRunner, err := runner.New(runner.Config{
		AppName:        "relay-run-turn-test",
		Agent:          relayAgent,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sess, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "relay-run-turn-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	return adkRunner, sess.Session.ID()
}
