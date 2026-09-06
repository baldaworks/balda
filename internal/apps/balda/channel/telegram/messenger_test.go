package telegram

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramfmt"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

type fakeChatActionClient struct {
	client.ClientWithResponsesInterface
	chatActions          []client.SendChatActionJSONRequestBody
	chatActionResults    []sendChatActionResult
	chatActionContexts   []context.Context
	messages             []client.SendMessageJSONRequestBody
	drafts               []client.SendMessageDraftJSONRequestBody
	richMessages         []client.SendRichMessageJSONRequestBody
	richDrafts           []client.SendRichMessageDraftJSONRequestBody
	sendMessageResults   []sendMessageResult
	sendRichResults      []sendRichMessageResult
	sendRichDraftResults []sendRichDraftResult
	messageContexts      []context.Context
}

type sendChatActionResult struct {
	resp *client.SendChatActionResponse
	err  error
}

type sendMessageResult struct {
	resp *client.SendMessageResponse
	err  error
}

type sendRichMessageResult struct {
	resp *client.SendRichMessageResponse
	err  error
}

type sendRichDraftResult struct {
	resp *client.SendRichMessageDraftResponse
	err  error
}

func successfulSendMessageResponse(messageID int) *client.SendMessageResponse {
	return &client.SendMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendMessage200Ok `json:"ok"`
			Result client.Message          `json:"result"`
		}{
			Ok:     true,
			Result: client.Message{MessageId: messageID},
		},
	}
}

func successfulSendRichMessageResponse(messageID int) *client.SendRichMessageResponse {
	return &client.SendRichMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendRichMessage200Ok `json:"ok"`
			Result client.Message              `json:"result"`
		}{
			Ok:     true,
			Result: client.Message{MessageId: messageID},
		},
	}
}

func (f *fakeChatActionClient) SendChatActionWithResponse(
	ctx context.Context,
	body client.SendChatActionJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendChatActionResponse, error) {
	f.chatActionContexts = append(f.chatActionContexts, ctx)
	f.chatActions = append(f.chatActions, body)
	if len(f.chatActionResults) > 0 {
		result := f.chatActionResults[0]
		f.chatActionResults = f.chatActionResults[1:]
		return result.resp, result.err
	}
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

func (f *fakeChatActionClient) SendMessageWithResponse(
	ctx context.Context,
	body client.SendMessageJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendMessageResponse, error) {
	f.messageContexts = append(f.messageContexts, ctx)
	f.messages = append(f.messages, body)
	if len(f.sendMessageResults) > 0 {
		result := f.sendMessageResults[0]
		f.sendMessageResults = f.sendMessageResults[1:]
		return result.resp, result.err
	}
	return successfulSendMessageResponse(len(f.messages)), nil
}

func (f *fakeChatActionClient) SendRichMessageWithResponse(
	ctx context.Context,
	body client.SendRichMessageJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendRichMessageResponse, error) {
	f.messageContexts = append(f.messageContexts, ctx)
	f.richMessages = append(f.richMessages, body)
	if len(f.sendRichResults) > 0 {
		result := f.sendRichResults[0]
		f.sendRichResults = f.sendRichResults[1:]
		return result.resp, result.err
	}
	return successfulSendRichMessageResponse(len(f.richMessages)), nil
}

func (f *fakeChatActionClient) SendMessageDraftWithResponse(
	ctx context.Context,
	body client.SendMessageDraftJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendMessageDraftResponse, error) {
	f.messageContexts = append(f.messageContexts, ctx)
	f.drafts = append(f.drafts, body)
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

func (f *fakeChatActionClient) SendRichMessageDraftWithResponse(
	ctx context.Context,
	body client.SendRichMessageDraftJSONRequestBody,
	_ ...client.RequestEditorFn,
) (*client.SendRichMessageDraftResponse, error) {
	f.messageContexts = append(f.messageContexts, ctx)
	f.richDrafts = append(f.richDrafts, body)
	if len(f.sendRichDraftResults) > 0 {
		result := f.sendRichDraftResults[0]
		f.sendRichDraftResults = f.sendRichDraftResults[1:]
		return result.resp, result.err
	}
	return &client.SendRichMessageDraftResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendRichMessageDraft200Ok `json:"ok"`
			Result bool                             `json:"result"`
		}{
			Ok:     true,
			Result: true,
		},
	}, nil
}

func TestMessengerDebugLogsDoNotIncludeMessageContent(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	tgClient := &fakeChatActionClient{}
	logger := zerolog.New(&logs).Level(zerolog.DebugLevel)
	m := NewMessenger(tgClient, logger)

	if err := m.SendDraftPlain(context.Background(), 9001, 1, "secret draft text", 0); err != nil {
		t.Fatalf("SendDraftPlain() error = %v", err)
	}
	m.SetAgentReplyFormattingMode(telegramfmt.ModeRichHTML)
	if err := m.SendAgentReply(context.Background(), 9001, "secret reply text", 0); err != nil {
		t.Fatalf("SendAgentReply() error = %v", err)
	}

	got := logs.String()
	for _, forbidden := range []string{"secret draft text", "secret reply text"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("debug logs contain message content %q: %s", forbidden, got)
		}
	}
	for _, want := range []string{"rich_payload_bytes", "draft_text_bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("debug logs missing safe metadata %q: %s", want, got)
		}
	}
}

func TestSendPlainUsesBoundedSendContext(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendPlain(context.Background(), 9001, "hello", 0); err != nil {
		t.Fatalf("SendPlain() error = %v", err)
	}

	if len(tgClient.messageContexts) != 1 {
		t.Fatalf("message contexts = %d, want 1", len(tgClient.messageContexts))
	}
	assertContextDeadlineWithin(t, tgClient.messageContexts[0], telegramSendTimeout)
}

func TestSendPlain_IncludesMessageThreadIDWhenTopicProvided(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendPlain(context.Background(), 9001, "hello", 77); err != nil {
		t.Fatalf("SendPlain() error = %v", err)
	}

	if len(tgClient.messages) != 1 {
		t.Fatalf("message calls = %d, want 1", len(tgClient.messages))
	}
	got := tgClient.messages[0]
	if got.ChatId != 9001 || got.Text != "hello" || got.ParseMode != nil {
		t.Fatalf("plain message = %+v", got)
	}
	if got.MessageThreadId == nil || *got.MessageThreadId != 77 {
		t.Fatalf("message_thread_id = %v, want 77", got.MessageThreadId)
	}
}

func TestSendPlain_ReturnsResponderError(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{
		sendMessageResults: []sendMessageResult{
			{
				resp: &client.SendMessageResponse{
					HTTPResponse: &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
					JSON400:      &client.ErrorResponse{Description: "Bad Request: chat not found"},
				},
			},
		},
	}
	m := NewMessenger(tgClient, zerolog.Nop())
	m.SetTelegramFormattingMode(telegramfmt.ModeNone)

	err := m.SendPlain(context.Background(), 9001, "hello", 0)
	if err == nil {
		t.Fatal("SendPlain() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "send message") {
		t.Fatalf("SendPlain() error = %v, want responder send error", err)
	}
}

func TestSendAgentReplyUsesBoundedSendContextForTransportFailure(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{
		sendRichResults: []sendRichMessageResult{
			{err: errors.New("network timeout")},
		},
	}
	m := NewMessenger(tgClient, zerolog.Nop())
	m.SetAgentReplyFormattingMode(telegramfmt.ModeRichHTML)

	err := m.SendAgentReply(context.Background(), 9001, "hello", 77)
	kind, classified := deliverycmd.ClassifyError(err)
	if !classified || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("SendAgentReply() error = %v, want ambiguous", err)
	}

	if len(tgClient.messageContexts) != 1 {
		t.Fatalf("message contexts = %d, want one bounded attempt", len(tgClient.messageContexts))
	}
	for _, ctx := range tgClient.messageContexts {
		assertContextDeadlineWithin(t, ctx, telegramSendTimeout)
	}
}

func TestSendChatActionPreservesParentCancellation(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.SendChatAction(ctx, 9001, 0, "typing"); err != nil {
		t.Fatalf("SendChatAction() error = %v", err)
	}

	if len(tgClient.chatActionContexts) != 1 {
		t.Fatalf("chat action contexts = %d, want 1", len(tgClient.chatActionContexts))
	}
	if got := tgClient.chatActionContexts[0].Err(); !errors.Is(got, context.Canceled) {
		t.Fatalf("chat action context err = %v, want context.Canceled", got)
	}
}

func assertContextDeadlineWithin(t *testing.T, ctx context.Context, maxDuration time.Duration) {
	t.Helper()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("context deadline already expired: remaining=%s", remaining)
	}
	if remaining > maxDuration {
		t.Fatalf("context deadline remaining = %s, want <= %s", remaining, maxDuration)
	}
}

func TestSendChatAction_IncludesMessageThreadIDWhenTopicProvided(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendChatAction(context.Background(), 9001, 77, "typing"); err != nil {
		t.Fatalf("SendChatAction() error = %v", err)
	}

	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chatActions calls = %d, want 1", len(tgClient.chatActions))
	}
	got := tgClient.chatActions[0]
	if got.ChatId != 9001 {
		t.Fatalf("chat_id = %d, want 9001", got.ChatId)
	}
	if got.Action != "typing" {
		t.Fatalf("action = %q, want typing", got.Action)
	}
	if got.MessageThreadId == nil || *got.MessageThreadId != 77 {
		t.Fatalf("message_thread_id = %v, want 77", got.MessageThreadId)
	}
}

func TestSendChatAction_OmitsMessageThreadIDForRootChat(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendChatAction(context.Background(), 9001, 0, "typing"); err != nil {
		t.Fatalf("SendChatAction() error = %v", err)
	}

	if len(tgClient.chatActions) != 1 {
		t.Fatalf("chatActions calls = %d, want 1", len(tgClient.chatActions))
	}
	if tgClient.chatActions[0].MessageThreadId != nil {
		t.Fatalf("message_thread_id = %v, want nil", tgClient.chatActions[0].MessageThreadId)
	}
}

func TestSendChatAction_AllowsEmptySuccessResponseBody(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{
		chatActionResults: []sendChatActionResult{
			{
				resp: &client.SendChatActionResponse{
					HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
				},
			},
		},
	}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendChatAction(context.Background(), -5173524191, 0, "typing"); err != nil {
		t.Fatalf("SendChatAction() error = %v, want nil", err)
	}
}

func TestSendChatAction_ReturnsTelegramErrorResponse(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{
		chatActionResults: []sendChatActionResult{
			{
				resp: &client.SendChatActionResponse{
					HTTPResponse: &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
					JSON400:      &client.ErrorResponse{Description: "Bad Request: chat not found"},
				},
			},
		},
	}
	m := NewMessenger(tgClient, zerolog.Nop())

	err := m.SendChatAction(context.Background(), 9001, 0, "typing")
	if err == nil {
		t.Fatal("SendChatAction() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("SendChatAction() error = %v, want chat not found", err)
	}
}

func TestSendMarkdown_DoesNotSplitStandaloneSeparator(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	if err := m.SendMarkdown(context.Background(), 9001, "first\n---\nsecond", 77); err != nil {
		t.Fatalf("SendMarkdown() error = %v", err)
	}

	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich messages calls = %d, want 1", len(tgClient.richMessages))
	}
	if tgClient.richMessages[0].RichMessage.Markdown == nil {
		t.Fatal("rich markdown = nil, want markdown payload")
	}
	got := *tgClient.richMessages[0].RichMessage.Markdown
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("rich markdown = %q, want both sections in one message", got)
	}
}

func TestSendAgentReply_RichMarkdownPreservesStandaloneSeparator(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{}
	m := NewMessenger(tgClient, zerolog.Nop())

	const input = "**first**\n\n---\n\nsecond"
	result, err := m.SendAgentReplyWithResult(context.Background(), 9001, input, 77)
	if err != nil {
		t.Fatalf("SendAgentReplyWithResult() error = %v", err)
	}

	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich message calls = %d, want 1", len(tgClient.richMessages))
	}
	if result.FirstMessageID != 1 || result.LastMessageID != 1 || result.MessageCount != 1 {
		t.Fatalf("result = %+v, want first=1 last=1 count=1", result)
	}
	got := tgClient.richMessages[0]
	if got.MessageThreadId == nil || *got.MessageThreadId != 77 {
		t.Fatalf("rich message message_thread_id = %v, want 77", got.MessageThreadId)
	}
	if got.RichMessage.Markdown == nil {
		t.Fatal("rich markdown = nil, want payload")
	}
	if payload := *got.RichMessage.Markdown; payload != input {
		t.Fatalf("rich markdown = %q, want original input", payload)
	}
}

func TestSendAgentReply_RichMarkdownFallsBackToPlainText(t *testing.T) {
	t.Parallel()

	const providerSecret = "TELEGRAM-PROVIDER-ERROR-SENTINEL"
	tgClient := &fakeChatActionClient{
		sendRichResults: []sendRichMessageResult{
			{
				resp: &client.SendRichMessageResponse{
					HTTPResponse: &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
					JSON400:      &client.ErrorResponse{Description: "Bad Request: can't parse entities " + providerSecret},
				},
			},
		},
	}
	var logs bytes.Buffer
	m := NewMessenger(tgClient, zerolog.New(&logs))

	const input = "**final**\n\n---\n\n![bad](https://example.invalid/missing.png)"
	result, err := m.SendAgentReplyWithResult(context.Background(), 9001, input, 77)
	if err != nil {
		t.Fatalf("SendAgentReplyWithResult() error = %v", err)
	}

	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich message calls = %d, want 1 failed attempt", len(tgClient.richMessages))
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("legacy message calls = %d, want 1 fallback", len(tgClient.messages))
	}
	if tgClient.messages[0].ParseMode != nil {
		t.Fatalf("fallback parse_mode = %v, want nil", *tgClient.messages[0].ParseMode)
	}
	if tgClient.messages[0].Text != input {
		t.Fatalf("fallback text = %q, want original input", tgClient.messages[0].Text)
	}
	if result.FirstMessageID != 1 || result.LastMessageID != 1 || result.MessageCount != 1 {
		t.Fatalf("result = %+v, want fallback message metadata", result)
	}
	gotLogs := logs.String()
	for _, secret := range []string{input, providerSecret} {
		if strings.Contains(gotLogs, secret) {
			t.Fatalf("fallback diagnostics contain sentinel %q: %s", secret, gotLogs)
		}
	}
	for _, field := range []string{
		`"settlement_class":"format_rejected"`,
		`"fallback":"plain"`,
		`"http_status":400`,
	} {
		if !strings.Contains(gotLogs, field) {
			t.Fatalf("fallback diagnostics missing %s: %s", field, gotLogs)
		}
	}
}

func TestSendAgentReply_RichMarkdownTransportErrorDoesNotFallbackToPlainText(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{
		sendRichResults: []sendRichMessageResult{
			{err: context.DeadlineExceeded},
		},
	}
	m := NewMessenger(tgClient, zerolog.Nop())

	_, err := m.SendAgentReplyWithResult(context.Background(), 9001, "**final**", 77)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendAgentReplyWithResult() error = %v, want deadline exceeded", err)
	}
	kind, classified := deliverycmd.ClassifyError(err)
	if !classified || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("SendAgentReplyWithResult() error = %v, want ambiguous", err)
	}

	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich message calls = %d, want 1 failed attempt", len(tgClient.richMessages))
	}
	if len(tgClient.messages) != 0 {
		t.Fatalf("legacy message calls = %d, want 0 on transport error", len(tgClient.messages))
	}
}

func TestTelegramDeliveryErrorsAreClassifiedAndRedacted(t *testing.T) {
	t.Parallel()

	const token = "123456:ABCdefGhIjkLMNopQRST_uvwx"
	cause := errors.New("Post https://api.telegram.org/bot" + token + "/sendMessage: timeout")
	err := telegramTransportError("send message", 9001, cause)
	kind, classified := deliverycmd.ClassifyError(err)
	if !classified || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("transport error = %v, want ambiguous", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("transport error = %v, want original cause preserved", err)
	}
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[REDACTED_TOKEN]") {
		t.Fatalf("transport error = %q, want token redacted", err)
	}

	noRespErr := telegramNoResponseError("send message", 9001)
	kind, classified = deliverycmd.ClassifyError(noRespErr)
	if !classified || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("no response error = %v, want ambiguous", noRespErr)
	}

	tests := []struct {
		status int
		want   deliverycmd.ErrorKind
	}{
		{status: http.StatusBadRequest, want: deliverycmd.ErrorKindPermanent},
		{status: http.StatusUnauthorized, want: deliverycmd.ErrorKindPermanent},
		{status: http.StatusForbidden, want: deliverycmd.ErrorKindPermanent},
		{status: http.StatusNotFound, want: deliverycmd.ErrorKindPermanent},
		{status: http.StatusTooManyRequests, want: deliverycmd.ErrorKindRetryable},
		{status: http.StatusTooEarly, want: deliverycmd.ErrorKindRetryable},
		{status: http.StatusBadGateway, want: deliverycmd.ErrorKindAmbiguous},
		{status: http.StatusGatewayTimeout, want: deliverycmd.ErrorKindAmbiguous},
		{status: http.StatusInternalServerError, want: deliverycmd.ErrorKindAmbiguous},
		{status: http.StatusRequestTimeout, want: deliverycmd.ErrorKindAmbiguous},
		{status: 0, want: deliverycmd.ErrorKindAmbiguous},
	}
	for _, tt := range tests {
		err := telegramHTTPError("send message", 9001, tt.status, "provider response")
		kind, classified := deliverycmd.ClassifyError(err)
		if !classified || kind != tt.want {
			t.Errorf("telegramHTTPError(status=%d) = %v kind=%q classified=%t, want %q", tt.status, err, kind, classified, tt.want)
		}
	}
}

func TestSendAgentReplySupportsCurrentFormattingModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mode   string
		text   string
		assert func(*testing.T, *fakeChatActionClient)
	}{
		{
			name: "rich markdown",
			mode: telegramfmt.ModeRichMarkdown,
			text: "**final**",
			assert: func(t *testing.T, client *fakeChatActionClient) {
				if len(client.richMessages) != 1 || client.richMessages[0].RichMessage.Markdown == nil || *client.richMessages[0].RichMessage.Markdown != "**final**" {
					t.Fatalf("rich markdown requests = %+v", client.richMessages)
				}
			},
		},
		{
			name: "rich html",
			mode: telegramfmt.ModeRichHTML,
			text: "<b>final</b> <script>unsafe</script>",
			assert: func(t *testing.T, client *fakeChatActionClient) {
				want := "<b>final</b> &lt;script&gt;unsafe&lt;/script&gt;"
				if len(client.richMessages) != 1 || client.richMessages[0].RichMessage.Html == nil || *client.richMessages[0].RichMessage.Html != want {
					t.Fatalf("rich html requests = %+v, want %q", client.richMessages, want)
				}
			},
		},
		{
			name: "plain",
			mode: telegramfmt.ModeNone,
			text: "<b>literal</b>",
			assert: func(t *testing.T, client *fakeChatActionClient) {
				if len(client.messages) != 1 || client.messages[0].Text != "<b>literal</b>" || client.messages[0].ParseMode != nil {
					t.Fatalf("plain requests = %+v", client.messages)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tgClient := &fakeChatActionClient{}
			messenger := NewMessenger(tgClient, zerolog.Nop())
			messenger.SetAgentReplyFormattingMode(test.mode)
			if err := messenger.SendAgentReply(context.Background(), 9001, test.text, 77); err != nil {
				t.Fatalf("SendAgentReply() error = %v", err)
			}
			test.assert(t, tgClient)
		})
	}
}

func TestRichHTMLFormattingRejectionFallsBackOnceToSafePlainText(t *testing.T) {
	t.Parallel()

	tgClient := &fakeChatActionClient{sendRichResults: []sendRichMessageResult{{resp: &client.SendRichMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
		JSON400:      &client.ErrorResponse{Description: "Bad Request: invalid HTML in rich message"},
	}}}}
	messenger := NewMessenger(tgClient, zerolog.Nop())
	message := deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichHTML,
		Text:          "<b>safe</b>&lt;script&gt;alert(1)&lt;/script&gt;",
		PlainFallback: "safealert(1)",
	}
	if _, err := messenger.SendAgentReplyMessageWithResult(context.Background(), 9001, message, 0); err != nil {
		t.Fatalf("SendAgentReplyMessageWithResult() error = %v", err)
	}
	if len(tgClient.richMessages) != 1 || len(tgClient.messages) != 1 {
		t.Fatalf("rich attempts = %d, plain attempts = %d", len(tgClient.richMessages), len(tgClient.messages))
	}
	if got := tgClient.messages[0]; got.Text != "safealert(1)" || got.ParseMode != nil {
		t.Fatalf("plain fallback = %+v", got)
	}
}

func TestRichMessageDoesNotFallbackWithoutExplicitFormattingRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result sendRichMessageResult
	}{
		{name: "ambiguous transport", result: sendRichMessageResult{err: context.DeadlineExceeded}},
		{name: "non-format bad request", result: sendRichMessageResult{resp: &client.SendRichMessageResponse{HTTPResponse: &http.Response{StatusCode: http.StatusBadRequest}, JSON400: &client.ErrorResponse{Description: "Bad Request: chat not found"}}}},
		{name: "unauthorized", result: sendRichMessageResult{resp: &client.SendRichMessageResponse{HTTPResponse: &http.Response{StatusCode: http.StatusUnauthorized}, JSON401: &client.ErrorResponse{Description: "Unauthorized"}}}},
		{name: "rate limited", result: sendRichMessageResult{resp: &client.SendRichMessageResponse{HTTPResponse: &http.Response{StatusCode: http.StatusTooManyRequests}}}},
		{name: "provider failure", result: sendRichMessageResult{resp: &client.SendRichMessageResponse{HTTPResponse: &http.Response{StatusCode: http.StatusBadGateway}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tgClient := &fakeChatActionClient{sendRichResults: []sendRichMessageResult{test.result}}
			messenger := NewMessenger(tgClient, zerolog.Nop())
			_, err := messenger.SendAgentReplyWithResult(context.Background(), 9001, "**final**", 0)
			if err == nil {
				t.Fatal("SendAgentReplyWithResult() error = nil, want non-nil")
			}
			if len(tgClient.messages) != 0 {
				t.Fatalf("plain fallback attempts = %d, want 0", len(tgClient.messages))
			}
		})
	}
}
