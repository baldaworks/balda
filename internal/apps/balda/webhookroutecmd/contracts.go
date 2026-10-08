// Package webhookroutecmd defines transport-neutral Backoffice webhook route values.
package webhookroutecmd

import (
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

var (
	ErrInvalid     = errors.New("invalid webhook route")
	ErrForbidden   = errors.New("webhook route operation forbidden")
	ErrNotFound    = errors.New("webhook route not found")
	ErrConflict    = errors.New("webhook route conflict")
	ErrUnavailable = errors.New("webhook route operation unavailable")
)

// ManagedSecretHeader carries the generated secret on managed external requests.
const ManagedSecretHeader = "X-Balda-Webhook-Secret"

const (
	// AuthTypeNone accepts requests without a route-level credential.
	AuthTypeNone = "none"
	// AuthTypeHeader validates one HTTP header value.
	AuthTypeHeader = "header"
	// DedupeSourceRequestID uses the request identifier as the dedupe key.
	DedupeSourceRequestID = "request_id"
	// DedupeSourceHeader uses one request header when present.
	DedupeSourceHeader = "header"
	// DedupeSourceBodySHA hashes the opaque request body.
	DedupeSourceBodySHA = "body_sha256"
)

// Authority binds an operation to the current administrator browser session.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Definition contains the editable fields of a managed webhook route.
// ReportTo is one optional public locator or managed alias name.
type Definition struct {
	Name           string
	Path           string
	PromptTemplate string
	ReportTo       string
	AckOnDelivery  bool
	DedupeSource   string
	DedupeHeader   string
}

// Item is a secret-free route view. Deleted items remain readable by name.
type Item struct {
	Definition   Definition
	Source       string
	Enabled      bool
	Deleted      bool
	Version      uint64
	ReportToKind string
	ReportToKey  string
	AuthType     string
	AuthHeader   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Create requests a managed route with a generated shared secret.
type Create struct {
	Definition Definition
	Authority  Authority
}

// Update replaces editable fields at one selected version.
type Update struct {
	Name            string
	ExpectedVersion uint64
	Definition      Definition
	Authority       Authority
}

// ChangeSelection enables or disables future admissions.
type ChangeSelection struct {
	Name            string
	ExpectedVersion uint64
	Enabled         bool
	Authority       Authority
}

// Delete archives one managed route at the selected version.
type Delete struct {
	Name            string
	ExpectedVersion uint64
	Authority       Authority
}

// Rotate replaces the secret verifier at the selected version.
type Rotate struct {
	Name            string
	ExpectedVersion uint64
	Authority       Authority
}

// SecretResult is returned only by Create and Rotate. Secret must never be
// persisted in a flash message, URL, audit event, or ordinary route read.
type SecretResult struct {
	Item   Item
	Secret string
}

// ConfiguredRoute is non-secret metadata of one host-configured route.
// Its auth value deliberately has no field here.
type ConfiguredRoute struct {
	Name           string
	Path           string
	PromptTemplate string
	ReportToKind   string
	ReportToKey    string
	AckOnDelivery  bool
	DedupeSource   string
	DedupeHeader   string
	AuthType       string
	AuthHeader     string
	Enabled        bool
}

const (
	// SourceConfig marks metadata owned by host configuration.
	SourceConfig = "config"
	// SourceManaged marks a Backoffice-owned route definition.
	SourceManaged = "managed"
)

// Record is the durable route value shared by management and its store port.
// SecretVerifier is populated only for ingress lookup, not List or Get.
type Record struct {
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

// MutationKind identifies a guarded managed route write.
type MutationKind string

const (
	MutationCreate    MutationKind = "create"
	MutationEdit      MutationKind = "edit"
	MutationSelection MutationKind = "selection"
	MutationDelete    MutationKind = "delete"
	MutationRotate    MutationKind = "rotate"
)

// Mutation couples a route write to browser authority and security audit.
type Mutation struct {
	Kind            MutationKind
	Record          Record
	ExpectedVersion uint64
	Authority       Authority
	Audit           usercmd.AuditEvent
}
