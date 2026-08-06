package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hupe1980/vecgo"
	"github.com/hupe1980/vecgo/metadata"
	"github.com/normahq/balda/sessionmemory"
)

const (
	// VecgoRecallProjectionSchema pins the application-owned generation
	// contract independently of Vecgo's internal manifest format.
	VecgoRecallProjectionSchema = "session-memory-vecgo/v1"
	vecgoActiveGenerationFile   = ".active-generation"
	vecgoGenerationDirectory    = "generations"
	vecgoCommittedFile          = ".committed"
	vecgoManifestFile           = "manifest.json"
	vecgoMappingFile            = "mapping.json"
	vecgoGenerationBuilding     = "building"
	vecgoGenerationDirty        = "dirty"
	vecgoGenerationCommitted    = "committed"
	vecgoGenerationClean        = "clean"
)

const (
	vecgoMetadataScopeKey       = "scope_key"
	vecgoMetadataScopeKind      = "scope_kind"
	vecgoMetadataItemID         = "item_id"
	vecgoMetadataRevisionID     = "revision_id"
	vecgoMetadataRevision       = "revision"
	vecgoMetadataKind           = "kind"
	vecgoMetadataCategory       = "category"
	vecgoMetadataMemoryKey      = "memory_key"
	vecgoMetadataScopeChangeSeq = "scope_change_seq"
)

// VecgoRecallProjectionConfig is versioned embedding/index configuration.
// Enabled is deliberately false by default; callers must opt in to creating
// any Vecgo files.
type VecgoRecallProjectionConfig struct {
	Enabled       bool   `json:"enabled"`
	SchemaVersion string `json:"schema_version"`
	ModelVersion  string `json:"model_version"`
	Dimension     int    `json:"dimension"`
	Metric        string `json:"metric"`
}

// DefaultVecgoRecallProjectionConfig returns the safe disabled configuration.
func DefaultVecgoRecallProjectionConfig() VecgoRecallProjectionConfig {
	return VecgoRecallProjectionConfig{SchemaVersion: VecgoRecallProjectionSchema, Metric: "cosine"}
}

func normalizeVecgoConfig(config VecgoRecallProjectionConfig) (VecgoRecallProjectionConfig, error) {
	if config.SchemaVersion == "" {
		config.SchemaVersion = VecgoRecallProjectionSchema
	}
	if config.Metric == "" {
		config.Metric = "cosine"
	}
	config.Metric = strings.ToLower(strings.TrimSpace(config.Metric))
	if config.SchemaVersion != VecgoRecallProjectionSchema || strings.TrimSpace(config.ModelVersion) == "" || config.Dimension < 1 || config.Dimension > sessionmemory.MaxVectorDimensions {
		return VecgoRecallProjectionConfig{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo projection configuration is invalid", nil)
	}
	switch config.Metric {
	case "l2", "cosine", "dot":
	default:
		return VecgoRecallProjectionConfig{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo projection metric is invalid", nil)
	}
	return config, nil
}

func vecgoMetric(config VecgoRecallProjectionConfig) vecgo.Metric {
	switch config.Metric {
	case "l2":
		return vecgo.MetricL2
	case "dot":
		return vecgo.MetricDot
	default:
		return vecgo.MetricCosine
	}
}

// VecgoRecallProjection is a local, disposable semantic candidate adapter.
// Canonical Badger hydration remains responsible for all lifecycle and
// forgetting decisions; Vecgo IDs are never returned to callers.
type VecgoRecallProjection struct {
	mu       sync.RWMutex
	root     string
	config   VecgoRecallProjectionConfig
	activeID string
	active   *vecgo.DB
}

type vecgoGenerationManifest struct {
	SchemaVersion string                      `json:"schema_version"`
	GenerationID  string                      `json:"generation_id"`
	Status        string                      `json:"status"`
	Watermark     uint64                      `json:"watermark"`
	UpdatedAt     time.Time                   `json:"updated_at"`
	Config        VecgoRecallProjectionConfig `json:"config"`
}

type vecgoMappingEntry struct {
	RevisionID string   `json:"revision_id"`
	BackendID  vecgo.ID `json:"backend_id"`
}

// VecgoGeneration is a write-once build generation. Dirty is persisted before
// the first batch, Vecgo Commit is explicit, and the clean watermark is
// persisted only after the commit and revision mapping are durable.
type VecgoGeneration struct {
	mu        sync.Mutex
	root      string
	path      string
	id        string
	config    VecgoRecallProjectionConfig
	db        *vecgo.DB
	mapping   map[string]vecgo.ID
	dirty     bool
	committed bool
	closed    bool
	watermark uint64
}

// OpenOptionalVecgoRecallProjection leaves the adapter disabled without
// touching the filesystem when config.Enabled is false.
func OpenOptionalVecgoRecallProjection(ctx context.Context, root string, config VecgoRecallProjectionConfig) (*VecgoRecallProjection, error) {
	if !config.Enabled {
		return nil, nil
	}
	return OpenVecgoRecallProjection(ctx, root, config)
}

// OpenVecgoRecallProjection opens the active clean generation, if one exists.
// A dirty or incompatible active marker fails closed instead of serving an
// ambiguous generation.
func OpenVecgoRecallProjection(ctx context.Context, root string, config VecgoRecallProjectionConfig) (*VecgoRecallProjection, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	config, err := normalizeVecgoConfig(config)
	if err != nil {
		return nil, err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo projection root is required", nil)
	}
	if err := os.MkdirAll(filepath.Join(root, vecgoGenerationDirectory), 0o700); err != nil {
		return nil, fmt.Errorf("create vecgo projection root: %w", err)
	}
	projection := &VecgoRecallProjection{root: root, config: config}
	marker, err := os.ReadFile(filepath.Join(root, vecgoActiveGenerationFile))
	if errors.Is(err, os.ErrNotExist) {
		return projection, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read vecgo active generation: %w", err)
	}
	id := strings.TrimSpace(string(marker))
	if !validVecgoGenerationID(id) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo active generation marker is invalid", nil)
	}
	path := filepath.Join(root, vecgoGenerationDirectory, id)
	manifest, err := readVecgoManifest(path)
	if err != nil {
		return nil, err
	}
	if manifest.Status != vecgoGenerationClean || manifest.Watermark == 0 || !sameVecgoConfig(manifest.Config, config) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo active generation is not clean or compatible", nil)
	}
	if _, err := os.Stat(filepath.Join(path, vecgoCommittedFile)); err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo active generation is not committed", err)
	}
	if _, err := os.Stat(filepath.Join(path, vecgoMappingFile)); err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo active generation mapping is missing", err)
	}
	db, err := vecgo.Open(ctx, vecgo.Local(path))
	if err != nil {
		return nil, fmt.Errorf("open vecgo active generation: %w", err)
	}
	projection.activeID = id
	projection.active = db
	return projection, nil
}

// NewGeneration starts a disposable generation with the configured embedding
// contract. Existing directories are never reused after a failed build.
func (p *VecgoRecallProjection) NewGeneration(ctx context.Context, id string) (*VecgoGeneration, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "vecgo projection is unavailable", nil)
	}
	if !validVecgoGenerationID(id) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo generation id is invalid", nil)
	}
	p.mu.RLock()
	root, config := p.root, p.config
	p.mu.RUnlock()
	path := filepath.Join(root, vecgoGenerationDirectory, id)
	if _, err := os.Stat(path); err == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo generation already exists", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect vecgo generation: %w", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create vecgo generation: %w", err)
	}
	db, err := vecgo.Open(ctx, vecgo.Local(path), vecgo.Create(config.Dimension, vecgoMetric(config)))
	if err != nil {
		return nil, fmt.Errorf("create vecgo generation: %w", err)
	}
	generation := &VecgoGeneration{root: root, path: path, id: id, config: config, db: db, mapping: make(map[string]vecgo.ID)}
	if err := generation.writeManifestLocked(vecgoGenerationManifest{SchemaVersion: VecgoRecallProjectionSchema, GenerationID: id, Status: vecgoGenerationBuilding, Config: config, UpdatedAt: time.Now().UTC()}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return generation, nil
}

// Index inserts one validated vector document. The dirty marker is written
// before Vecgo sees the batch, so a crash cannot advertise partial work.
func (g *VecgoGeneration) Index(ctx context.Context, document sessionmemory.VectorProjectionDocument) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if g == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "vecgo generation is unavailable", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkWritableLocked(); err != nil {
		return err
	}
	if err := document.Validate(g.config.ModelVersion, g.config.Dimension); err != nil {
		return err
	}
	if _, exists := g.mapping[document.RevisionID]; exists {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo revision is already indexed", nil)
	}
	if !g.dirty {
		if err := g.writeManifestLocked(g.manifestLocked(vecgoGenerationDirty)); err != nil {
			return err
		}
		g.dirty = true
	}
	metadataDocument := vecgoMetadata(document)
	backendID, err := g.db.Insert(ctx, document.Embedding, metadataDocument, nil)
	if err != nil {
		return fmt.Errorf("insert vecgo vector: %w", err)
	}
	g.mapping[document.RevisionID] = backendID
	return nil
}

// Delete removes one vector from a still-building generation. Canonical
// forgetting remains authoritative; this method only keeps a disposable
// generation tidy during a rebuild.
func (g *VecgoGeneration) Delete(ctx context.Context, revisionID string) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(revisionID) == "" {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo revision id is required", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkWritableLocked(); err != nil {
		return err
	}
	backendID, exists := g.mapping[revisionID]
	if !exists {
		return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "vecgo revision is not indexed", nil)
	}
	if err := g.db.Delete(ctx, backendID); err != nil {
		return fmt.Errorf("delete vecgo vector: %w", err)
	}
	delete(g.mapping, revisionID)
	return nil
}

// Commit durably flushes Vecgo and writes the committed generation marker.
// The generation remains dirty until AdvanceWatermark succeeds.
func (g *VecgoGeneration) Commit(ctx context.Context) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if g == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "vecgo generation is unavailable", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkOpenLocked(); err != nil {
		return err
	}
	if g.committed {
		return nil
	}
	if err := g.db.Commit(ctx); err != nil {
		return fmt.Errorf("commit vecgo generation: %w", err)
	}
	if err := os.WriteFile(filepath.Join(g.path, vecgoCommittedFile), []byte(VecgoRecallProjectionSchema+"\n"), 0o600); err != nil {
		return fmt.Errorf("write vecgo committed marker: %w", err)
	}
	if err := writeVecgoMapping(filepath.Join(g.path, vecgoMappingFile), g.mapping); err != nil {
		return err
	}
	g.committed = true
	if err := g.writeManifestLocked(g.manifestLocked(vecgoGenerationCommitted)); err != nil {
		return err
	}
	return nil
}

// AdvanceWatermark records the canonical sequence only after Vecgo Commit and
// the RevisionID mapping have succeeded. This is the sole transition to clean.
func (g *VecgoGeneration) AdvanceWatermark(ctx context.Context, watermark uint64) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if watermark == 0 {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo watermark is required", nil)
	}
	if g == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "vecgo generation is unavailable", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkOpenLocked(); err != nil {
		return err
	}
	if !g.committed {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo watermark cannot advance before commit", nil)
	}
	if watermark < g.watermark {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo watermark cannot move backwards", nil)
	}
	g.watermark = watermark
	return g.writeManifestLocked(g.manifestLocked(vecgoGenerationClean))
}

// ActivateGeneration atomically advertises a committed clean generation.
func (p *VecgoRecallProjection) ActivateGeneration(ctx context.Context, id string) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if p == nil || !validVecgoGenerationID(id) {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "vecgo activation is invalid", nil)
	}
	p.mu.RLock()
	root, config := p.root, p.config
	p.mu.RUnlock()
	path := filepath.Join(root, vecgoGenerationDirectory, id)
	manifest, err := readVecgoManifest(path)
	if err != nil {
		return err
	}
	if manifest.Status != vecgoGenerationClean || manifest.Watermark == 0 || !sameVecgoConfig(manifest.Config, config) {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo generation is not clean or compatible", nil)
	}
	marker, err := os.ReadFile(filepath.Join(path, vecgoCommittedFile))
	if err != nil || strings.TrimSpace(string(marker)) != VecgoRecallProjectionSchema {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo generation is not durably committed", err)
	}
	if _, err := os.Stat(filepath.Join(path, vecgoMappingFile)); err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo generation mapping is missing", err)
	}
	db, err := vecgo.Open(ctx, vecgo.Local(path))
	if err != nil {
		return fmt.Errorf("open vecgo generation for activation: %w", err)
	}
	markerPath := filepath.Join(root, vecgoActiveGenerationFile)
	temporaryPath := markerPath + ".tmp"
	if err := os.WriteFile(temporaryPath, []byte(id+"\n"), 0o600); err != nil {
		_ = db.Close()
		return fmt.Errorf("write vecgo active marker: %w", err)
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		_ = os.Remove(temporaryPath)
		_ = db.Close()
		return fmt.Errorf("activate vecgo generation: %w", err)
	}
	p.mu.Lock()
	previous := p.active
	p.active = db
	p.activeID = id
	p.mu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

// SearchVector returns bounded metadata candidates from the active generation.
// Numeric Vecgo IDs are used only internally; RevisionID is read from the
// application metadata and subsequently hydrated from canonical storage.
func (p *VecgoRecallProjection) SearchVector(ctx context.Context, request sessionmemory.RecallRequest, embedding []float32) ([]sessionmemory.RecallProjectionHit, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	normalized, err := sessionmemory.NormalizeRecallRequest(request)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	db, config := p.active, p.config
	p.mu.RUnlock()
	if db == nil {
		return []sessionmemory.RecallProjectionHit{}, nil
	}
	if len(embedding) != config.Dimension {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidQuery, "query embedding dimension does not match vecgo projection", nil)
	}
	filters := []metadata.Filter{
		{Key: vecgoMetadataScopeKey, Operator: metadata.OpEqual, Value: metadata.String(normalized.Scope.Key)},
		{Key: vecgoMetadataScopeKind, Operator: metadata.OpEqual, Value: metadata.String(string(normalized.Scope.Kind))},
	}
	if normalized.Kind != nil {
		filters = append(filters, metadata.Filter{Key: vecgoMetadataKind, Operator: metadata.OpEqual, Value: metadata.String(string(*normalized.Kind))})
	}
	if normalized.Category != nil {
		filters = append(filters, metadata.Filter{Key: vecgoMetadataCategory, Operator: metadata.OpEqual, Value: metadata.String(string(*normalized.Category))})
	}
	if normalized.MemoryKey != "" {
		filters = append(filters, metadata.Filter{Key: vecgoMetadataMemoryKey, Operator: metadata.OpEqual, Value: metadata.String(string(normalized.MemoryKey))})
	}
	candidateLimit := normalized.Limit * 4
	if candidateLimit > sessionmemory.MaxRecallCandidates {
		candidateLimit = sessionmemory.MaxRecallCandidates
	}
	candidates, err := db.Search(ctx, embedding, candidateLimit, vecgo.WithFilter(metadata.NewFilterSet(filters...)), vecgo.WithoutData(), vecgo.WithMetadata())
	if err != nil {
		return nil, fmt.Errorf("search vecgo recall projection: %w", err)
	}
	hits := make([]sessionmemory.RecallProjectionHit, 0, len(candidates))
	for _, candidate := range candidates {
		hit, err := vecgoRecallHit(candidate, normalized.Scope, config.Metric)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Close releases the active generation.
func (p *VecgoRecallProjection) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	active := p.active
	p.active = nil
	p.activeID = ""
	p.mu.Unlock()
	if active == nil {
		return nil
	}
	return active.Close()
}

// Close closes a build generation without advertising it.
func (g *VecgoGeneration) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.db == nil {
		return nil
	}
	err := g.db.Close()
	g.db = nil
	return err
}

func (g *VecgoGeneration) checkWritableLocked() error {
	if err := g.checkOpenLocked(); err != nil {
		return err
	}
	if g.committed {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "vecgo generation was already committed", nil)
	}
	return nil
}

func (g *VecgoGeneration) checkOpenLocked() error {
	if g.closed || g.db == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo generation is closed", nil)
	}
	return nil
}

func (g *VecgoGeneration) manifestLocked(status string) vecgoGenerationManifest {
	return vecgoGenerationManifest{SchemaVersion: VecgoRecallProjectionSchema, GenerationID: g.id, Status: status, Watermark: g.watermark, Config: g.config, UpdatedAt: time.Now().UTC()}
}

func (g *VecgoGeneration) writeManifestLocked(manifest vecgoGenerationManifest) error {
	return writeVecgoManifest(filepath.Join(g.path, vecgoManifestFile), manifest)
}

func writeVecgoManifest(path string, manifest vecgoGenerationManifest) error {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode vecgo generation manifest: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write vecgo generation manifest: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("activate vecgo generation manifest: %w", err)
	}
	return nil
}

func readVecgoManifest(path string) (vecgoGenerationManifest, error) {
	encoded, err := os.ReadFile(filepath.Join(path, vecgoManifestFile))
	if err != nil {
		return vecgoGenerationManifest{}, fmt.Errorf("read vecgo generation manifest: %w", err)
	}
	var manifest vecgoGenerationManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return vecgoGenerationManifest{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo generation manifest is invalid", err)
	}
	if manifest.SchemaVersion != VecgoRecallProjectionSchema || manifest.GenerationID == "" {
		return vecgoGenerationManifest{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo generation manifest is invalid", nil)
	}
	return manifest, nil
}

func writeVecgoMapping(path string, mapping map[string]vecgo.ID) error {
	entries := make([]vecgoMappingEntry, 0, len(mapping))
	for revisionID, backendID := range mapping {
		entries = append(entries, vecgoMappingEntry{RevisionID: revisionID, BackendID: backendID})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RevisionID < entries[j].RevisionID })
	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode vecgo revision mapping: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write vecgo revision mapping: %w", err)
	}
	return nil
}

func sameVecgoConfig(left, right VecgoRecallProjectionConfig) bool {
	return left.SchemaVersion == right.SchemaVersion && left.ModelVersion == right.ModelVersion && left.Dimension == right.Dimension && left.Metric == right.Metric
}

func validVecgoGenerationID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\t")
}

func vecgoMetadata(document sessionmemory.VectorProjectionDocument) metadata.Document {
	category := ""
	if document.Category != nil {
		category = string(*document.Category)
	}
	return metadata.Document{
		vecgoMetadataScopeKey:       metadata.String(document.Scope.Key),
		vecgoMetadataScopeKind:      metadata.String(string(document.Scope.Kind)),
		vecgoMetadataItemID:         metadata.String(document.ItemID),
		vecgoMetadataRevisionID:     metadata.String(document.RevisionID),
		vecgoMetadataRevision:       metadata.Int(int64(document.Revision)),
		vecgoMetadataKind:           metadata.String(string(document.Kind)),
		vecgoMetadataCategory:       metadata.String(category),
		vecgoMetadataMemoryKey:      metadata.String(string(document.MemoryKey)),
		vecgoMetadataScopeChangeSeq: metadata.Int(int64(document.ScopeChangeSeq)),
	}
}

func vecgoRecallHit(candidate vecgo.Candidate, scope sessionmemory.Scope, metric string) (sessionmemory.RecallProjectionHit, error) {
	value, ok := candidate.Metadata[vecgoMetadataScopeKey]
	if !ok || value.StringValue() != scope.Key {
		return sessionmemory.RecallProjectionHit{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "vecgo hit scope metadata is invalid", nil)
	}
	scopeKind, ok := candidate.Metadata[vecgoMetadataScopeKind]
	if !ok || scopeKind.StringValue() != string(scope.Kind) {
		return sessionmemory.RecallProjectionHit{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "vecgo hit scope kind metadata is invalid", nil)
	}
	revisionID, ok := candidate.Metadata[vecgoMetadataRevisionID]
	if !ok || strings.TrimSpace(revisionID.StringValue()) == "" {
		return sessionmemory.RecallProjectionHit{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo hit revision metadata is missing", nil)
	}
	itemID := candidate.Metadata[vecgoMetadataItemID].StringValue()
	revision, revisionOK := candidate.Metadata[vecgoMetadataRevision].AsInt64()
	changeSeq, changeOK := candidate.Metadata[vecgoMetadataScopeChangeSeq].AsInt64()
	if itemID == "" || !revisionOK || revision <= 0 || !changeOK || changeSeq <= 0 {
		return sessionmemory.RecallProjectionHit{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "vecgo hit metadata is incomplete", nil)
	}
	score := float64(candidate.Score)
	if metric == "l2" {
		score = 1 / (1 + score)
	}
	if score < 0 {
		score = 0
	}
	return sessionmemory.RecallProjectionHit{Scope: scope, ItemID: itemID, RevisionID: revisionID.StringValue(), Revision: uint64(revision), Score: score, ScopeChangeSeq: uint64(changeSeq)}, nil
}
