package attachmentstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

type localStore struct {
	root string
}

func newLocalStore(stateDir string) (Store, error) {
	root := filepath.Join(strings.TrimSpace(stateDir), "attachments")
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("attachment local store requires balda.state_dir")
	}
	return &localStore{root: root}, nil
}

func (s *localStore) Enabled() bool { return true }

func (s *localStore) Persist(ctx context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error) {
	item, ok := attachment.Normalize(descriptor)
	if !ok {
		return attachment.Descriptor{}, fmt.Errorf("persist attachment: invalid descriptor")
	}
	if item.Blob != nil && strings.TrimSpace(item.Blob.Path) != "" {
		return item, nil
	}
	if body == nil {
		return attachment.Descriptor{}, fmt.Errorf("persist attachment: body is required")
	}
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return attachment.Descriptor{}, fmt.Errorf("persist attachment: max bytes must be between 1 and %d", int64(math.MaxInt64-1))
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("create attachment root: %w", err)
	}

	tmp, err := os.CreateTemp(s.root, ".incoming-*")
	if err != nil {
		return attachment.Descriptor{}, fmt.Errorf("create attachment temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	tmpClosed := false
	keepTemp := false
	defer func() {
		if !tmpClosed {
			_ = tmp.Close()
		}
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	hash := sha256.New()
	limited := io.LimitReader(contextReader{ctx: ctx, reader: body}, maxBytes+1)
	size, err := io.Copy(io.MultiWriter(tmp, hash), limited)
	if err != nil {
		return attachment.Descriptor{}, fmt.Errorf("write attachment temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("persist attachment: %w", err)
	}
	if size > maxBytes {
		return attachment.Descriptor{}, fmt.Errorf("%w: limit %d bytes", ErrTooLarge, maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("sync attachment temporary file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("set attachment permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("close attachment temporary file: %w", err)
	}
	tmpClosed = true

	shaText := hex.EncodeToString(hash.Sum(nil))
	key := filepath.Join(shaText[:2], shaText[2:4], shaText+extensionFromDescriptor(item))
	absPath := filepath.Join(s.root, key)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("create attachment directory: %w", err)
	}
	if info, statErr := os.Stat(absPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return attachment.Descriptor{}, fmt.Errorf("attachment blob path is not a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return attachment.Descriptor{}, fmt.Errorf("inspect attachment blob: %w", statErr)
	} else if err := os.Rename(tmpPath, absPath); err != nil {
		return attachment.Descriptor{}, fmt.Errorf("publish attachment blob: %w", err)
	} else {
		keepTemp = true
	}

	item.SizeBytes = size
	item.Blob = &attachment.BlobRef{
		Store:  EngineLocal,
		Key:    filepath.ToSlash(key),
		Path:   absPath,
		SHA256: shaText,
	}
	return item, nil
}

func extensionFromDescriptor(item attachment.Descriptor) string {
	name := strings.TrimSpace(item.FileName)
	if ext := safeExtension(filepath.Ext(name)); ext != "" {
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

func safeExtension(ext string) string {
	if len(ext) < 2 || len(ext) > 16 || ext[0] != '.' {
		return ""
	}
	for _, r := range ext[1:] {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		return ""
	}
	return ext
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}
