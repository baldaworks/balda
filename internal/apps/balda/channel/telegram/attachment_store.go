package telegram

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/tgbotkit/client"
)

const (
	// Telegram persistence did not previously impose a Balda-side size policy.
	// Keep that behavior while the shared store still requires an overflow-safe bound.
	maxTelegramAttachmentBytes = math.MaxInt64 - 1
	telegramDownloadTimeout    = 90 * time.Second
)

// BlobStore is the persistence capability consumed by the Telegram adapter.
type BlobStore interface {
	Enabled() bool
	Persist(ctx context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error)
}

type telegramFileDownloader interface {
	DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error)
}

type attachmentStore struct {
	blobs      BlobStore
	downloader telegramFileDownloader
}

// NewAttachmentStore adapts Telegram file retrieval to transport-neutral blob persistence.
func NewAttachmentStore(blobs BlobStore, tgClient client.ClientWithResponsesInterface, token string) AttachmentStore {
	return &attachmentStore{
		blobs: blobs,
		downloader: &telegramDownloader{
			client: tgClient,
			token:  strings.TrimSpace(token),
			http:   newTelegramDownloadHTTPClient(),
		},
	}
}

func (s *attachmentStore) PersistTelegram(ctx context.Context, descriptors []attachment.Descriptor) ([]attachment.Descriptor, error) {
	items := attachment.NormalizeList(descriptors)
	if len(items) == 0 {
		return nil, nil
	}
	if s == nil || s.blobs == nil {
		return nil, fmt.Errorf("telegram attachment blob store is unavailable")
	}
	if !s.blobs.Enabled() {
		return items, nil
	}
	if s.downloader == nil {
		return nil, fmt.Errorf("telegram attachment downloader is unavailable")
	}

	out := make([]attachment.Descriptor, 0, len(items))
	for _, item := range items {
		if item.Blob != nil && strings.TrimSpace(item.Blob.Path) != "" {
			out = append(out, item)
			continue
		}
		if strings.TrimSpace(item.FileID) == "" {
			out = append(out, item)
			continue
		}
		body, err := s.downloader.DownloadFile(ctx, item.FileID)
		if err != nil {
			return nil, err
		}
		persisted, persistErr := s.blobs.Persist(ctx, item, body, maxTelegramAttachmentBytes)
		_ = body.Close()
		if persistErr != nil {
			return nil, persistErr
		}
		out = append(out, persisted)
	}
	return attachment.NormalizeList(out), nil
}

type telegramDownloader struct {
	client client.ClientWithResponsesInterface
	token  string
	http   *http.Client
}

func (d *telegramDownloader) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	if d == nil || d.client == nil {
		return nil, fmt.Errorf("telegram downloader is unavailable")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, fmt.Errorf("telegram file_id is required")
	}
	resp, err := d.client.GetFileWithResponse(ctx, client.GetFileJSONRequestBody{FileId: fileID})
	if err != nil {
		return nil, fmt.Errorf("telegram getFile %s: %w", fileID, err)
	}
	if resp.JSON200 == nil || resp.JSON200.Result.FilePath == nil || strings.TrimSpace(*resp.JSON200.Result.FilePath) == "" {
		return nil, fmt.Errorf("telegram getFile %s returned no file path", fileID)
	}
	filePath := strings.TrimLeft(strings.TrimSpace(*resp.JSON200.Result.FilePath), "/")
	endpoint := (&url.URL{
		Scheme: "https",
		Host:   "api.telegram.org",
		Path:   "/file/bot" + d.token + "/" + filePath,
	}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build telegram file request: %w", err)
	}
	httpClient := d.http
	if httpClient == nil {
		httpClient = newTelegramDownloadHTTPClient()
	}
	httpResp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download telegram file %s: %w", fileID, err)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		_ = httpResp.Body.Close()
		return nil, fmt.Errorf("download telegram file %s: status %s", fileID, httpResp.Status)
	}
	return httpResp.Body, nil
}

func newTelegramDownloadHTTPClient() *http.Client {
	return &http.Client{
		Timeout: telegramDownloadTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.Hostname() != "api.telegram.org" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}
