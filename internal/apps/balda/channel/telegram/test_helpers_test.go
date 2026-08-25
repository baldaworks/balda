package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

type fakeTelegramClient struct {
	client.ClientWithResponsesInterface
	sendErr        error
	createTopicErr error
	closeTopicErr  error
	nextTopicID    int
	closedTopicIDs []int
	messages       []client.SendMessageJSONRequestBody
	richMessages   []client.SendRichMessageJSONRequestBody
	richDrafts     []client.SendRichMessageDraftJSONRequestBody
	createdTopics  []client.CreateForumTopicJSONRequestBody
}

func (c *fakeTelegramClient) SendMessageWithResponse(_ context.Context, body client.SendMessageJSONRequestBody, _ ...client.RequestEditorFn) (*client.SendMessageResponse, error) {
	c.messages = append(c.messages, body)
	if c.sendErr != nil {
		return nil, c.sendErr
	}
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

func (c *fakeTelegramClient) SendRichMessageWithResponse(_ context.Context, body client.SendRichMessageJSONRequestBody, _ ...client.RequestEditorFn) (*client.SendRichMessageResponse, error) {
	c.richMessages = append(c.richMessages, body)
	if c.sendErr != nil {
		return nil, c.sendErr
	}
	return &client.SendRichMessageResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.SendRichMessage200Ok `json:"ok"`
			Result client.Message              `json:"result"`
		}{
			Ok:     true,
			Result: client.Message{MessageId: len(c.richMessages)},
		},
	}, nil
}

func (c *fakeTelegramClient) SendRichMessageDraftWithResponse(_ context.Context, body client.SendRichMessageDraftJSONRequestBody, _ ...client.RequestEditorFn) (*client.SendRichMessageDraftResponse, error) {
	c.richDrafts = append(c.richDrafts, body)
	if c.sendErr != nil {
		return nil, c.sendErr
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

func (c *fakeTelegramClient) CreateForumTopicWithResponse(_ context.Context, body client.CreateForumTopicJSONRequestBody, _ ...client.RequestEditorFn) (*client.CreateForumTopicResponse, error) {
	c.createdTopics = append(c.createdTopics, body)
	if c.createTopicErr != nil {
		return nil, c.createTopicErr
	}
	if c.nextTopicID == 0 {
		c.nextTopicID = 123
	}
	return &client.CreateForumTopicResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.CreateForumTopic200Ok `json:"ok"`
			Result client.ForumTopic            `json:"result"`
		}{
			Ok:     true,
			Result: client.ForumTopic{MessageThreadId: c.nextTopicID},
		},
	}, nil
}

func (c *fakeTelegramClient) DeleteForumTopicWithResponse(_ context.Context, body client.DeleteForumTopicJSONRequestBody, _ ...client.RequestEditorFn) (*client.DeleteForumTopicResponse, error) {
	c.closedTopicIDs = append(c.closedTopicIDs, body.MessageThreadId)
	if c.closeTopicErr != nil {
		return nil, c.closeTopicErr
	}
	return &client.DeleteForumTopicResponse{
		HTTPResponse: &http.Response{StatusCode: http.StatusOK, Status: "200 OK"},
		JSON200: &struct {
			Ok     client.DeleteForumTopic200Ok `json:"ok"`
			Result bool                         `json:"result"`
		}{
			Ok:     true,
			Result: true,
		},
	}, nil
}

func newTestAdapter(tgClient client.ClientWithResponsesInterface, formattingMode string) *Adapter {
	msg := NewMessenger(tgClient, zerolog.Nop())
	if formattingMode != "" {
		msg.SetAgentReplyFormattingMode(formattingMode)
	}
	return NewAdapter(AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
}

func assertLastSentContains(t *testing.T, tgClient *fakeTelegramClient, wantSubstring string) {
	t.Helper()
	last := lastSentText(t, tgClient)
	if !strings.Contains(last, wantSubstring) {
		t.Fatalf("last sent text = %q, want substring %q", last, wantSubstring)
	}
}

func lastSentText(t *testing.T, tgClient *fakeTelegramClient) string {
	t.Helper()
	if len(tgClient.richMessages) > 0 {
		rich := tgClient.richMessages[len(tgClient.richMessages)-1].RichMessage
		switch {
		case rich.Markdown != nil:
			return *rich.Markdown
		case rich.Html != nil:
			return *rich.Html
		}
		return ""
	}
	if len(tgClient.messages) == 0 {
		t.Fatal("sent messages = 0, want at least one")
	}
	return tgClient.messages[len(tgClient.messages)-1].Text
}
