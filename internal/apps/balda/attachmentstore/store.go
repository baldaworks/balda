package attachmentstore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

const (
	EngineOff   = "off"
	EngineLocal = "local"
)

var (
	// ErrDisabled reports that persistence was requested while the store is off.
	ErrDisabled = attachment.ErrStoreDisabled
	// ErrTooLarge reports that content exceeded the caller's byte limit.
	ErrTooLarge = attachment.ErrTooLarge
)

type Config struct {
	Engine   string
	StateDir string
}

// Store persists already downloaded attachment content.
type Store interface {
	Enabled() bool
	Persist(ctx context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error)
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

// New constructs the configured attachment store.
func New(cfg Config) (Store, error) {
	switch NormalizeEngine(cfg.Engine) {
	case EngineOff:
		return noopStore{}, nil
	case EngineLocal:
		return newLocalStore(cfg.StateDir)
	default:
		return nil, fmt.Errorf("unsupported attachment store engine %q", cfg.Engine)
	}
}

type noopStore struct{}

func (noopStore) Enabled() bool { return false }

func (noopStore) Persist(context.Context, attachment.Descriptor, io.Reader, int64) (attachment.Descriptor, error) {
	return attachment.Descriptor{}, ErrDisabled
}
