package state

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	WebhookRouteSourceConfig  = "config"
	WebhookRouteSourceManaged = "managed"
)

var (
	ErrWebhookRouteInvalid     = errors.New("invalid webhook route")
	ErrWebhookRouteConflict    = errors.New("webhook route conflict")
	ErrWebhookRouteNotFound    = errors.New("webhook route not found")
	ErrWebhookRouteForbidden   = errors.New("webhook route forbidden")
	ErrWebhookRouteUnavailable = errors.New("webhook route store unavailable")
)

// WebhookRouteRecord is durable route metadata. SecretVerifier is set only by
// LookupByPath and must never be rendered in a Backoffice read model.
type WebhookRouteRecord struct {
	Name, Source, Path, PromptTemplate string
	ReportToKind, ReportToKey          string
	AckOnDelivery                      bool
	DedupeSource, DedupeHeader         string
	AuthType, AuthHeader               string
	SecretVerifier                     string
	Enabled, Deleted                   bool
	Version                            uint64
	CreatedAt, UpdatedAt               time.Time
}

// WebhookRouteAuthority fences an administrator browser family at commit time.
type WebhookRouteAuthority struct {
	UserID, SessionID                                          string
	UserVersion, CredentialVersion, MFAVersion, SessionVersion uint64
	At                                                         time.Time
}

// WebhookRouteMutationKind selects a managed route write.
type WebhookRouteMutationKind string

const (
	WebhookRouteCreate    WebhookRouteMutationKind = "create"
	WebhookRouteEdit      WebhookRouteMutationKind = "edit"
	WebhookRouteSelection WebhookRouteMutationKind = "selection"
	WebhookRouteDelete    WebhookRouteMutationKind = "delete"
	WebhookRouteRotate    WebhookRouteMutationKind = "rotate"
)

// WebhookRouteMutation couples a versioned route write with browser authority and audit.
type WebhookRouteMutation struct {
	Kind            WebhookRouteMutationKind
	Record          WebhookRouteRecord
	ExpectedVersion uint64
	Authority       WebhookRouteAuthority
	Audit           usercmd.AuditEvent
}

// WebhookRouteStore persists source metadata and guarded managed definitions.
type WebhookRouteStore interface {
	Get(ctx context.Context, name string) (WebhookRouteRecord, bool, error)
	List(ctx context.Context) ([]WebhookRouteRecord, error)
	LookupByPath(ctx context.Context, path string) (WebhookRouteRecord, bool, error)
	ReconcileConfig(ctx context.Context, routes []WebhookRouteRecord) error
	CheckAuthority(ctx context.Context, authority WebhookRouteAuthority) error
	Save(ctx context.Context, mutation WebhookRouteMutation) error
}
