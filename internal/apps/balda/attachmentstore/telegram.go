package attachmentstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tgbotkit/client"
)

type TelegramDownloader struct {
	client client.ClientWithResponsesInterface
	token  string
}

func NewTelegramDownloader(tgClient client.ClientWithResponsesInterface, token string) *TelegramDownloader {
	return &TelegramDownloader{client: tgClient, token: strings.TrimSpace(token)}
}

func (d *TelegramDownloader) DownloadFile(ctx context.Context, fileID string) (DownloadedFile, error) {
	if d == nil || d.client == nil {
		return DownloadedFile{}, fmt.Errorf("telegram downloader is unavailable")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return DownloadedFile{}, fmt.Errorf("telegram file_id is required")
	}
	resp, err := d.client.GetFileWithResponse(ctx, client.GetFileJSONRequestBody{FileId: fileID})
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("telegram getFile %s: %w", fileID, err)
	}
	if resp.JSON200 == nil || resp.JSON200.Result.FilePath == nil || strings.TrimSpace(*resp.JSON200.Result.FilePath) == "" {
		return DownloadedFile{}, fmt.Errorf("telegram getFile %s returned no file path", fileID)
	}
	filePath := strings.TrimSpace(*resp.JSON200.Result.FilePath)
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", d.token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("build telegram file request: %w", err)
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("download telegram file %s: %w", fileID, err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return DownloadedFile{}, fmt.Errorf("download telegram file %s: status %s", fileID, httpResp.Status)
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("read telegram file body: %w", err)
	}
	return DownloadedFile{
		FileID:       fileID,
		FileUniqueID: strings.TrimSpace(resp.JSON200.Result.FileUniqueId),
		SizeBytes:    int64(len(body)),
		Body:         body,
	}, nil
}
