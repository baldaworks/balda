package attachmentstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/attachment"
)

const (
	EngineOff   = "off"
	EngineLocal = "local"
)

type Config struct {
	Engine   string
	StateDir string
}

type TelegramFileDownloader interface {
	DownloadFile(ctx context.Context, fileID string) (DownloadedFile, error)
}

type DownloadedFile struct {
	FileID       string
	FileUniqueID string
	FileName     string
	MIMEType     string
	SizeBytes    int64
	Body         []byte
}

type Store interface {
	PersistTelegram(ctx context.Context, in []attachment.Descriptor) ([]attachment.Descriptor, error)
}

func NormalizeEngine(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", EngineOff:
		return EngineOff
	case EngineLocal:
		return EngineLocal
	default:
		return EngineOff
	}
}

func New(cfg Config, downloader TelegramFileDownloader) (Store, error) {
	switch NormalizeEngine(cfg.Engine) {
	case EngineOff:
		return noopStore{}, nil
	case EngineLocal:
		return newLocalStore(cfg.StateDir, downloader)
	default:
		return nil, fmt.Errorf("unsupported attachment store engine %q", cfg.Engine)
	}
}

type noopStore struct{}

func (noopStore) PersistTelegram(_ context.Context, in []attachment.Descriptor) ([]attachment.Descriptor, error) {
	return attachment.NormalizeList(in), nil
}
