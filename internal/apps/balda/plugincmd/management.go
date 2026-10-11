package plugincmd

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid         = errors.New("invalid plugin operation")
	ErrForbidden       = errors.New("plugin operation forbidden")
	ErrConflict        = errors.New("plugin state changed")
	ErrNotFound        = errors.New("plugin not found")
	ErrUnavailable     = errors.New("plugin unavailable")
	ErrRecoveryPending = errors.New("plugin activation accepted; recovery pending")
)

// Authority is populated by the host from its authenticated browser session.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Operation identifies an explicit supported browser lifecycle action.
type Operation string

const (
	Install  Operation = "install"
	Upgrade  Operation = "upgrade"
	Enable   Operation = "enable"
	Disable  Operation = "disable"
	Rollback Operation = "rollback"
	Remove   Operation = "remove"
)

// PluginMutation carries selectors and untrusted observed-state comparisons.
type PluginMutation struct {
	Operation       Operation
	PluginName      string
	MarketplaceName string
	CandidateToken  string
	Revision        string
	ExpectedState   string
	Confirm         bool
	Authority       Authority
}

// Validate rejects ambiguous selectors before expensive source preparation.
func (r PluginMutation) Validate() error {
	if strings.TrimSpace(r.PluginName) == "" || r.ExpectedState == "" {
		return ErrInvalid
	}
	switch r.Operation {
	case Install:
		if r.MarketplaceName == "" || r.CandidateToken == "" || r.Revision != "" {
			return ErrInvalid
		}
	case Upgrade:
		if r.MarketplaceName != "" || r.CandidateToken == "" || r.Revision != "" {
			return ErrInvalid
		}
	case Enable, Disable, Remove:
		if r.MarketplaceName != "" || r.CandidateToken != "" || r.Revision != "" {
			return ErrInvalid
		}
	case Rollback:
		if r.MarketplaceName != "" || r.CandidateToken != "" || r.Revision == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if (r.Operation == Disable || r.Operation == Rollback || r.Operation == Remove) && !r.Confirm {
		return ErrInvalid
	}
	return nil
}

// Observation binds durable state without carrying source content or paths.
// Digests must be calculated from the complete owner-defined canonical record.
type Observation struct {
	Identity        string
	RecordDigest    string
	Generation      string
	RevisionsDigest string
}

// Digest returns a stable opaque comparison value for canonical private data.
func Digest(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Token serializes only a digest, never the private observed record.
func (o Observation) Token() string {
	data, _ := json.Marshal(o)
	return Digest(string(data))
}

// Matches compares an untrusted token with the current complete observation.
func (o Observation) Matches(token string) bool {
	return subtle.ConstantTimeCompare([]byte(o.Token()), []byte(token)) == 1
}

// MarketplaceAdd contains transient source input; it must never be reflected.
type MarketplaceAdd struct {
	Name      string
	Source    string
	Ref       string
	Sparse    []string
	Authority Authority
}

// MarketplaceMutation selects one observed configured definition.
type MarketplaceMutation struct {
	Name          string
	ExpectedState string
	Confirm       bool
	Authority     Authority
}

// Contribution contains safe display metadata, never executable content.
type Contribution struct {
	Name        string
	Description string
	Transport   string
}

// RetainedRevision describes a selectable immutable plugin revision.
type RetainedRevision struct {
	Version    string
	Revision   string
	RetainedAt time.Time
	Available  bool
	Current    bool
}

// InstalledView describes durable selection separately from runtime health.
type InstalledView struct {
	PluginStatus
	ExpectedState   string
	OriginAvailable bool
	RecoveryPending bool
}

// PluginDetail adds safe retained contributions and revision choices.
type PluginDetail struct {
	InstalledView
	Commands        []Contribution
	Skills          []Contribution
	MCPServers      []Contribution
	Revisions       []RetainedRevision
	DiagnosticCodes []string
}

// CandidateView binds review to a particular configured source and content.
type CandidateView struct {
	Name            string
	Version         string
	Description     string
	Marketplace     string
	Revision        string
	CandidateToken  string
	ExpectedState   string
	Capabilities    CapabilitySummary
	DiagnosticCodes []string
}

// MarketplaceView projects accepted refresh evidence without filesystem data.
type MarketplaceView struct {
	Name              string
	Source            string
	Kind              string
	Ref               string
	Sparse            []string
	ExpectedState     string
	LastAttemptAt     time.Time
	LastAttemptStatus string
	FailureCode       string
	LastSuccessfulAt  time.Time
	ResolvedRevision  string
	AvailablePlugins  int
}

// MarketplaceDetail contains the last successful discovery, possibly stale.
type MarketplaceDetail struct {
	MarketplaceView
	Candidates      []CandidateView
	DiagnosticCodes []string
}

// MutationOutcome distinguishes durable acceptance from completed publication.
type MutationOutcome struct {
	PluginName      string
	Accepted        bool
	RecoveryPending bool
}
