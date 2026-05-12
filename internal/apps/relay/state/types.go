package state

import (
	"context"
	"time"

	"github.com/normahq/relay/internal/apps/relay/auth"
	"github.com/tgbotkit/runtime/updatepoller"
	adksession "google.golang.org/adk/session"
)

const (
	// NamespaceApp stores relay app internal state (for example owner auth).
	NamespaceApp = "relay.app"
	// NamespaceSessionMCP stores relay.state MCP key-value data.
	NamespaceSessionMCP = "relay.session_mcp"

	// SessionStatusActive marks a session that can be lazily restored.
	SessionStatusActive = "active"

	// ChannelTypeTelegram is the current relay channel type backed by Telegram.
	ChannelTypeTelegram = "telegram"
)

// Provider exposes relay state capabilities behind a backend-agnostic interface.
// This allows swapping SQLite with another provider later.
type Provider interface {
	AppKV() KVStore
	ADKSessions() adksession.Service
	SessionMCPKV() KVStore
	Sessions() SessionStore
	PollingOffsetStore() updatepoller.OffsetStore
	Collaborators() CollaboratorStore
	Close() error
}

// KVStore stores string and JSON key/value records.
type KVStore interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Clear(ctx context.Context) error
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	SetJSON(ctx context.Context, key string, value any) error
	SetWithTTL(ctx context.Context, key string, value any, ttl time.Duration) error
	MergeJSON(ctx context.Context, key string, fields map[string]any) (merged map[string]any, err error)
}

// CollaboratorStore persists authorized collaborators.
type CollaboratorStore interface {
	AddCollaborator(ctx context.Context, c auth.Collaborator) error
	RemoveCollaborator(ctx context.Context, userID string) error
	GetCollaborator(ctx context.Context, userID string) (*auth.Collaborator, bool, error)
	ListCollaborators(ctx context.Context) ([]auth.Collaborator, error)
}

// SessionRecord persists relay session metadata for lazy restore.
type SessionRecord struct {
	SessionID    string
	UserID       string
	ChannelType  string
	AddressKey   string
	AddressJSON  string
	AgentName    string
	WorkspaceDir string
	BranchName   string
	Status       string
}

// SessionStore persists relay session metadata.
type SessionStore interface {
	Upsert(ctx context.Context, record SessionRecord) error
	GetByAddress(ctx context.Context, channelType, addressKey string) (SessionRecord, bool, error)
	GetBySessionID(ctx context.Context, sessionID string) (SessionRecord, bool, error)
	DeleteBySessionID(ctx context.Context, sessionID string) error
	List(ctx context.Context) ([]SessionRecord, error)
}
