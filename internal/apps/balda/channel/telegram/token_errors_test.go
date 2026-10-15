package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

type failingBotAPIClient struct {
	client.ClientWithResponsesInterface
	err error
}

func (c failingBotAPIClient) EditMessageReplyMarkupWithResponse(context.Context, client.EditMessageReplyMarkupJSONRequestBody, ...client.RequestEditorFn) (*client.EditMessageReplyMarkupResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) DeleteMessageWithResponse(context.Context, client.DeleteMessageJSONRequestBody, ...client.RequestEditorFn) (*client.DeleteMessageResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) DeleteEphemeralMessageWithResponse(context.Context, client.DeleteEphemeralMessageJSONRequestBody, ...client.RequestEditorFn) (*client.DeleteEphemeralMessageResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) AnswerCallbackQueryWithResponse(context.Context, client.AnswerCallbackQueryJSONRequestBody, ...client.RequestEditorFn) (*client.AnswerCallbackQueryResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) CreateForumTopicWithResponse(context.Context, client.CreateForumTopicJSONRequestBody, ...client.RequestEditorFn) (*client.CreateForumTopicResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) DeleteForumTopicWithResponse(context.Context, client.DeleteForumTopicJSONRequestBody, ...client.RequestEditorFn) (*client.DeleteForumTopicResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) GetMeWithResponse(context.Context, ...client.RequestEditorFn) (*client.GetMeResponse, error) {
	return nil, c.err
}

func (c failingBotAPIClient) GetFileWithResponse(context.Context, client.GetFileJSONRequestBody, ...client.RequestEditorFn) (*client.GetFileResponse, error) {
	return nil, c.err
}

func TestTelegramBotAPIFailuresRedactTransportErrors(t *testing.T) {
	const token = "123456:ABCdefGhIjkLMNopQRST_uvwx"
	cause := errors.New("Post https://api.telegram.org/bot" + token + "/method: connection reset")
	api := failingBotAPIClient{err: cause}
	messenger := NewMessenger(api, zerolog.Nop())
	adapter := &Adapter{tgClient: api}
	server := &Server{tgClient: api}
	downloader := &telegramDownloader{client: api}

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"clear inline keyboard", func() error { return messenger.ClearInlineKeyboard(t.Context(), 9001, 12) }},
		{"delete message", func() error { return messenger.DeleteMessage(t.Context(), 9001, 12) }},
		{"delete ephemeral message", func() error { return messenger.DeleteEphemeralMessage(t.Context(), 9001, 2, 12) }},
		{"answer callback query", func() error { return messenger.AnswerCallbackQuery(t.Context(), "callback", "", false) }},
		{"create forum topic", func() error { _, err := adapter.CreateTopicLocator(t.Context(), 9001, "topic"); return err }},
		{"delete forum topic", func() error { return adapter.Close(t.Context(), NewLocator(9001, 12)) }},
		{"get bot identity", func() error { return server.initializeBotUsername(t.Context()) }},
		{"get file", func() error { _, err := downloader.DownloadFile(t.Context(), "file"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "connection reset") {
				t.Fatalf("%s error = %v, want sanitized transport context", tc.name, err)
			}
			if errors.Is(err, cause) {
				t.Fatalf("%s error retains credential-bearing cause", tc.name)
			}
		})
	}
}

type failingDownloadTransport struct{}

func (failingDownloadTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset")
}

type filePathBotAPIClient struct {
	client.ClientWithResponsesInterface
	filePath string
}

func (c filePathBotAPIClient) GetFileWithResponse(context.Context, client.GetFileJSONRequestBody, ...client.RequestEditorFn) (*client.GetFileResponse, error) {
	return &client.GetFileResponse{JSON200: &struct {
		Ok     client.GetFile200Ok `json:"ok"`
		Result client.File         `json:"result"`
	}{Result: client.File{FilePath: &c.filePath}}}, nil
}

func TestTelegramFileDownloadErrorRedactsBotToken(t *testing.T) {
	const token = "123456:ABCdefGhIjkLMNopQRST_uvwx"
	downloader := &telegramDownloader{
		client: filePathBotAPIClient{filePath: "documents/report.pdf"},
		token:  token,
		http:   &http.Client{Transport: failingDownloadTransport{}},
	}
	body, err := downloader.DownloadFile(t.Context(), "file")
	if body != nil {
		_ = body.Close()
		t.Fatal("DownloadFile() returned body after transport failure")
	}
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("DownloadFile() error = %v, want sanitized transport context", err)
	}
}
