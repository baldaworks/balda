package state

import (
	"context"
	"encoding/json"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// PluginObservation is the complete durable comparison baseline for one plugin.
// Private source and archive fields must never be projected to browser DTOs.
type PluginObservation struct {
	OriginIdentity string
	PluginID       string
	Install        *PluginInstallRecord
	Revisions      []PluginRevisionRecord
	Pending        []PluginActivationIntent
	Generation     uint64
}

// Token hashes the complete private baseline, including absence and generations.
func (o PluginObservation) Token() string {
	data, _ := json.Marshal(o)
	return plugincmd.Digest(string(data))
}

// MarketplaceObservation retains owner-defined definition and discovery JSON.
// SQL storage neither parses sources nor decides discovery freshness.
type MarketplaceObservation struct {
	Name           string
	DefinitionJSON json.RawMessage
	MetadataJSON   json.RawMessage
	Generation     uint64
}

// Token hashes the complete private marketplace baseline.
func (o MarketplaceObservation) Token() string {
	data, _ := json.Marshal(o)
	return plugincmd.Digest(string(data))
}

// PluginCommit describes one atomic logical activation or deactivation.
// Nil Install means removal; Revision may introduce newly staged metadata.
type PluginCommit struct {
	Authority                *plugincmd.Authority
	PluginID                 string
	ExpectedState            string
	MarketplaceName          string
	ExpectedMarketplaceState string
	Install                  *PluginInstallRecord
	Revision                 *PluginRevisionRecord
	Intent                   PluginActivationIntent
	OriginIdentity           *string
	AuditID                  string
}

// MarketplaceCommit atomically replaces definition/discovery and its generation.
// Nil DefinitionJSON removes a definition while retaining its generation tombstone.
type MarketplaceCommit struct {
	Authority      *plugincmd.Authority
	Name           string
	ExpectedState  string
	DefinitionJSON json.RawMessage
	MetadataJSON   json.RawMessage
	Operation      string
	AuditID        string
	At             time.Time
}

// PluginManagementStore fences plugin/marketplace acceptance and canonical audit.
// Trusted variants are for already-authorized owner lifecycle calls, never HTTP.
type PluginManagementStore interface {
	CheckAuthority(ctx context.Context, authority plugincmd.Authority) error
	ObservePlugin(ctx context.Context, pluginID string) (PluginObservation, error)
	ObserveMarketplace(ctx context.Context, name string) (MarketplaceObservation, error)
	CommitPlugin(ctx context.Context, mutation PluginCommit) error
	CommitPluginTrusted(ctx context.Context, mutation PluginCommit) error
	PurgePluginRevisionTrusted(ctx context.Context, pluginID, revisionID, expectedState, auditID string, at time.Time) error
	CommitMarketplace(ctx context.Context, mutation MarketplaceCommit) error
	CommitMarketplaceTrusted(ctx context.Context, mutation MarketplaceCommit) error
}
