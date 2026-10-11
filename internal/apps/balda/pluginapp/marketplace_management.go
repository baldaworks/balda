package pluginapp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
)

// ManagementStore is the consuming durable acceptance/observation port.
// Source interpretation and discovery remain owned by this application.
type ManagementStore interface {
	CheckAuthority(ctx context.Context, authority plugincmd.Authority) error
	ObserveMarketplace(ctx context.Context, name string) (state.MarketplaceObservation, error)
	CommitMarketplace(ctx context.Context, mutation state.MarketplaceCommit) error
	CommitMarketplaceTrusted(ctx context.Context, mutation state.MarketplaceCommit) error
	ObservePlugin(ctx context.Context, name string) (state.PluginObservation, error)
	CommitPlugin(ctx context.Context, mutation state.PluginCommit) error
	CommitPluginTrusted(ctx context.Context, mutation state.PluginCommit) error
}

const discoverySucceeded = "succeeded"
const discoveryFailed = "failed"
const discoveryUnavailable = "source_unavailable"
const discoveryInvalidIndex = "invalid_index"
const discoveryDuplicateName = "duplicate_plugin_name"
const discoveryInvalidEntry = "invalid_plugin_entry"

// discoverySnapshot is private accepted evidence, independent of cache files.
type discoverySnapshot struct {
	LastAttemptAt     time.Time
	LastAttemptStatus string
	FailureCode       string
	LastSuccessful    *discoverySuccess
}

type discoverySuccess struct {
	At               time.Time
	IndexName        string
	ResolvedRevision string
	Candidates       []discoveryCandidate
	DiagnosticCodes  []string
}

type discoveryCandidate struct {
	Installable     bool
	Name            string
	Version         string
	Description     string
	RelativePath    string
	ManifestPath    string
	Revision        string
	Capabilities    plugincmd.CapabilitySummary
	DiagnosticCodes []string
}

func (s *Service) lockOwner() func() {
	if s.managed != nil {
		s.managed.mu.Lock()
		return s.managed.mu.Unlock
	}
	s.ownerMu.Lock()
	return s.ownerMu.Unlock
}

func (s *Service) managementAuthority(ctx context.Context, a plugincmd.Authority) error {
	if s.management == nil {
		return plugincmd.ErrUnavailable
	}
	a.At = time.Now().UTC()
	return s.management.CheckAuthority(ctx, a)
}

// ManagementMarketplaces reads accepted metadata without fetching any sources.
func (s *Service) ManagementMarketplaces(ctx context.Context, a plugincmd.Authority) ([]plugincmd.MarketplaceView, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return nil, err
	}
	unlock := s.lockOwner()
	defer unlock()
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	out := make([]plugincmd.MarketplaceView, 0, len(sources))
	for _, src := range sources {
		detail, err := s.marketplaceDetailLocked(ctx, src.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, detail.MarketplaceView)
	}
	return out, nil
}

// ManagementMarketplace reads the last accepted discovery, even after failure.
func (s *Service) ManagementMarketplace(ctx context.Context, name string, a plugincmd.Authority) (plugincmd.MarketplaceDetail, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	unlock := s.lockOwner()
	defer unlock()
	return s.marketplaceDetailLocked(ctx, name)
}

// ManagementAddMarketplace accepts a unique definition without fetching it.
func (s *Service) ManagementAddMarketplace(ctx context.Context, r plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error) {
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	src := normalizeSource(MarketplaceSource{Name: r.Name, Source: r.Source, Ref: r.Ref, Sparse: append([]string(nil), r.Sparse...)})
	if err := s.validateManagedMarketplace(src); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	unlock := s.lockOwner()
	defer unlock()
	observed, err := s.management.ObserveMarketplace(ctx, src.Name)
	if err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	if observed.DefinitionJSON != nil {
		return plugincmd.MarketplaceDetail{}, plugincmd.ErrConflict
	}
	raw, _ := json.Marshal(src)
	a := r.Authority
	a.At = time.Now().UTC()
	if err := s.management.CommitMarketplace(ctx, state.MarketplaceCommit{Authority: &a, Name: src.Name, ExpectedState: observed.Token(), DefinitionJSON: raw, Operation: "add", AuditID: uuid.NewString(), At: a.At}); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	return s.marketplaceDetailLocked(ctx, src.Name)
}

// ManagementRefreshMarketplace explicitly prepares and accepts a discovery attempt.
func (s *Service) ManagementRefreshMarketplace(ctx context.Context, r plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error) {
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	if !safeMarketplaceName(r.Name) || r.ExpectedState == "" {
		return plugincmd.MarketplaceDetail{}, plugincmd.ErrInvalid
	}
	unlock := s.lockOwner()
	defer unlock()
	observed, src, old, err := s.marketplaceStateLocked(ctx, r.Name)
	if err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	if observed.Token() != r.ExpectedState {
		return plugincmd.MarketplaceDetail{}, plugincmd.ErrConflict
	}
	success, code := s.discoverMarketplaceLocked(ctx, src)
	a := r.Authority
	a.At = time.Now().UTC()
	metadata := acceptedDiscovery(old, success, code, a.At)
	raw, _ := json.Marshal(metadata)
	operation := "refresh"
	if code != "" {
		operation = "refresh-failed"
	}
	if err := s.management.CommitMarketplace(ctx, state.MarketplaceCommit{Authority: &a, Name: r.Name, ExpectedState: observed.Token(), DefinitionJSON: observed.DefinitionJSON, MetadataJSON: raw, Operation: operation, AuditID: uuid.NewString(), At: a.At}); err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	detail, err := s.marketplaceDetailLocked(ctx, r.Name)
	if err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	if code != "" {
		return detail, plugincmd.ErrUnavailable
	}
	return detail, nil
}

// ManagementRemoveMarketplace removes only a confirmed configured definition.
func (s *Service) ManagementRemoveMarketplace(ctx context.Context, r plugincmd.MarketplaceMutation) error {
	if err := s.managementAuthority(ctx, r.Authority); err != nil {
		return err
	}
	if !safeMarketplaceName(r.Name) || !r.Confirm || r.ExpectedState == "" {
		return plugincmd.ErrInvalid
	}
	unlock := s.lockOwner()
	defer unlock()
	observed, _, _, err := s.marketplaceStateLocked(ctx, r.Name)
	if err != nil {
		return err
	}
	if observed.Token() != r.ExpectedState {
		return plugincmd.ErrConflict
	}
	a := r.Authority
	a.At = time.Now().UTC()
	return s.management.CommitMarketplace(ctx, state.MarketplaceCommit{Authority: &a, Name: r.Name, ExpectedState: observed.Token(), Operation: "remove", AuditID: uuid.NewString(), At: a.At})
}

func (s *Service) marketplaceStateLocked(ctx context.Context, name string) (state.MarketplaceObservation, MarketplaceSource, discoverySnapshot, error) {
	var src MarketplaceSource
	var metadata discoverySnapshot
	if !safeMarketplaceName(name) {
		return state.MarketplaceObservation{}, src, metadata, plugincmd.ErrInvalid
	}
	observed, err := s.management.ObserveMarketplace(ctx, name)
	if err != nil {
		return observed, src, metadata, err
	}
	if observed.DefinitionJSON == nil {
		return observed, src, metadata, plugincmd.ErrNotFound
	}
	if json.Unmarshal(observed.DefinitionJSON, &src) != nil || src.Name != name || s.validateManagedMarketplace(src) != nil {
		return observed, src, metadata, plugincmd.ErrUnavailable
	}
	if observed.MetadataJSON != nil && (json.Unmarshal(observed.MetadataJSON, &metadata) != nil || !validDiscoveryMetadata(metadata)) {
		return observed, src, metadata, plugincmd.ErrUnavailable
	}
	return observed, src, metadata, nil
}

func (s *Service) marketplaceDetailLocked(ctx context.Context, name string) (plugincmd.MarketplaceDetail, error) {
	observed, src, metadata, err := s.marketplaceStateLocked(ctx, name)
	if err != nil {
		return plugincmd.MarketplaceDetail{}, err
	}
	kind := marketplaceSourceKindGit
	if resolveLocalMarketplaceRoot(src.Source) != "" {
		kind = marketplaceSourceKindLocal
	}
	view := plugincmd.MarketplaceView{Name: src.Name, Source: safeMarketplaceSource(src.Source, kind), Kind: string(kind), Ref: src.Ref, Sparse: append([]string(nil), src.Sparse...), ExpectedState: observed.Token(), LastAttemptAt: metadata.LastAttemptAt, LastAttemptStatus: metadata.LastAttemptStatus, FailureCode: metadata.FailureCode}
	detail := plugincmd.MarketplaceDetail{MarketplaceView: view}
	if metadata.LastSuccessful == nil {
		return detail, nil
	}
	success := metadata.LastSuccessful
	detail.LastSuccessfulAt = success.At
	detail.ResolvedRevision = success.ResolvedRevision
	detail.AvailablePlugins = len(success.Candidates)
	detail.DiagnosticCodes = append([]string(nil), success.DiagnosticCodes...)
	for _, candidate := range success.Candidates {
		token := ""
		if metadata.LastAttemptStatus == discoverySucceeded && candidate.Installable {
			token = discoveryCandidateToken(observed, candidate)
		}
		detail.Candidates = append(detail.Candidates, plugincmd.CandidateView{Name: candidate.Name, Version: candidate.Version, Description: candidate.Description, Marketplace: src.Name, Revision: candidate.Revision, CandidateToken: token, Capabilities: candidate.Capabilities, DiagnosticCodes: append([]string(nil), candidate.DiagnosticCodes...)})
	}
	return detail, nil
}

func acceptedDiscovery(old discoverySnapshot, success *discoverySuccess, code string, at time.Time) discoverySnapshot {
	result := old
	result.LastAttemptAt = at
	result.FailureCode = code
	result.LastAttemptStatus = discoveryFailed
	if code == "" {
		success.At = at
		result.LastSuccessful = success
		result.LastAttemptStatus = discoverySucceeded
	}
	return result
}

func discoveryCandidateToken(observed state.MarketplaceObservation, c discoveryCandidate) string {
	raw, _ := json.Marshal(c)
	return plugincmd.Digest(observed.Token() + string(raw))
}

func safeMarketplaceName(name string) bool {
	if name == "" || len(name) > 64 || strings.Contains(name, "..") || name != strings.TrimSpace(name) {
		return false
	}
	for i, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if i > 0 && (c == '-' || c == '_' || c == '.') {
			continue
		}
		return false
	}
	return true
}
