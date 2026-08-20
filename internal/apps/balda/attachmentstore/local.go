package attachmentstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

type localStore struct {
	root       string
	downloader TelegramFileDownloader
}

func newLocalStore(stateDir string, downloader TelegramFileDownloader) (Store, error) {
	root := filepath.Join(strings.TrimSpace(stateDir), "attachments")
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("attachment local store requires balda.state_dir")
	}
	if downloader == nil {
		return nil, fmt.Errorf("attachment local store requires telegram downloader")
	}
	return &localStore{root: root, downloader: downloader}, nil
}

func (s *localStore) PersistTelegram(ctx context.Context, in []attachment.Descriptor) ([]attachment.Descriptor, error) {
	items := attachment.NormalizeList(in)
	if len(items) == 0 {
		return nil, nil
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
		downloaded, err := s.downloader.DownloadFile(ctx, item.FileID)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(downloaded.Body)
		shaText := hex.EncodeToString(sum[:])
		ext := filepath.Ext(strings.TrimSpace(downloaded.FileName))
		if ext == "" {
			ext = extensionFromDescriptor(item)
		}
		key := filepath.Join(shaText[:2], shaText[2:4], shaText+ext)
		absPath := filepath.Join(s.root, key)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return nil, fmt.Errorf("create attachment dir: %w", err)
		}
		if err := os.WriteFile(absPath, downloaded.Body, 0o644); err != nil {
			return nil, fmt.Errorf("write attachment blob: %w", err)
		}
		item.Blob = &attachment.BlobRef{
			Store:  EngineLocal,
			Key:    filepath.ToSlash(key),
			Path:   absPath,
			SHA256: shaText,
		}
		if item.FileName == "" {
			item.FileName = strings.TrimSpace(downloaded.FileName)
		}
		if item.MIMEType == "" {
			item.MIMEType = strings.TrimSpace(downloaded.MIMEType)
		}
		if item.SizeBytes <= 0 {
			item.SizeBytes = downloaded.SizeBytes
		}
		out = append(out, item)
	}
	return attachment.NormalizeList(out), nil
}

func extensionFromDescriptor(item attachment.Descriptor) string {
	name := strings.TrimSpace(item.FileName)
	if ext := filepath.Ext(name); ext != "" {
		return ext
	}
	switch strings.ToLower(strings.TrimSpace(item.MIMEType)) {
	case "audio/ogg":
		return ".ogg"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	}
	if item.Kind == attachment.KindVoice {
		return ".ogg"
	}
	return ""
}
