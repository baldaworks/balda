package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/en"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/ru"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/normahq/balda/sessionmemory"
)

const (
	// BleveRecallProjectionSchema pins the application-owned projection
	// document contract independently of Bleve's internal index metadata.
	BleveRecallProjectionSchema  = "session-memory-bleve/v1"
	bleveActiveGenerationFile    = ".active-generation"
	bleveCommittedGenerationFile = ".committed"
	bleveGenerationDirectory     = "generations"
	bleveBatchSize               = 128
)

const (
	bleveFieldTextStandard = "text_standard"
	bleveFieldTextRussian  = "text_russian"
	bleveFieldTextEnglish  = "text_english"
	bleveFieldScopeKey     = "scope_key"
	bleveFieldScopeKind    = "scope_kind"
	bleveFieldItemID       = "item_id"
	bleveFieldRevisionID   = "revision_id"
	bleveFieldRevision     = "revision"
	bleveFieldKind         = "kind"
	bleveFieldCategory     = "category"
	bleveFieldMemoryKey    = "memory_key"
	bleveFieldCreatedAt    = "created_at"
	bleveFieldSourceIDs    = "source_ids"
	bleveFieldSessionIDs   = "session_ids"
	bleveFieldChangeSeq    = "scope_change_seq"
)

// BleveRecallProjection is a rebuildable persistent lexical adapter. It
// stores only disposable projection documents; RecallService hydrates all
// returned IDs from canonical storage before exposing text.
type BleveRecallProjection struct {
	mu       sync.RWMutex
	root     string
	activeID string
	active   bleve.Index
}

// BleveGeneration is a write-once build generation. A generation must be
// committed and closed before it can be activated.
type BleveGeneration struct {
	mu        sync.Mutex
	id        string
	path      string
	index     bleve.Index
	batch     *bleve.Batch
	closed    bool
	committed bool
}

type bleveRecallDocument struct {
	SchemaVersion  string   `json:"schema_version"`
	ScopeKey       string   `json:"scope_key"`
	ScopeKind      string   `json:"scope_kind"`
	ItemID         string   `json:"item_id"`
	RevisionID     string   `json:"revision_id"`
	Revision       string   `json:"revision"`
	Kind           string   `json:"kind"`
	Category       string   `json:"category,omitempty"`
	MemoryKey      string   `json:"memory_key,omitempty"`
	TextStandard   string   `json:"text_standard"`
	TextRussian    string   `json:"text_russian"`
	TextEnglish    string   `json:"text_english"`
	CreatedAt      string   `json:"created_at"`
	SourceIDs      []string `json:"source_ids,omitempty"`
	SessionIDs     []string `json:"session_ids,omitempty"`
	ScopeChangeSeq string   `json:"scope_change_seq"`
}

// NewBleveRecallProjection opens the active generation under root. The root
// is created if necessary; with no active marker Search safely returns no
// projection candidates so callers can use the bounded canonical fallback.
func NewBleveRecallProjection(root string) (*BleveRecallProjection, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "bleve projection root is required", nil)
	}
	if err := os.MkdirAll(filepath.Join(root, bleveGenerationDirectory), 0o700); err != nil {
		return nil, fmt.Errorf("create bleve projection root: %w", err)
	}
	projection := &BleveRecallProjection{root: root}
	marker, err := os.ReadFile(filepath.Join(root, bleveActiveGenerationFile))
	if errors.Is(err, os.ErrNotExist) {
		return projection, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bleve active generation: %w", err)
	}
	id := strings.TrimSpace(string(marker))
	if !validBleveGenerationID(id) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve active generation marker is invalid", nil)
	}
	committed, err := os.ReadFile(filepath.Join(root, bleveGenerationDirectory, id, bleveCommittedGenerationFile))
	if err != nil || strings.TrimSpace(string(committed)) != BleveRecallProjectionSchema {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve active generation is not committed", err)
	}
	index, err := bleve.Open(filepath.Join(root, bleveGenerationDirectory, id))
	if err != nil {
		return nil, fmt.Errorf("open bleve active generation: %w", err)
	}
	projection.activeID = id
	projection.active = index
	return projection, nil
}

// NewGeneration starts a new disposable generation. Existing generation IDs
// are rejected to avoid silently mixing partial rebuilds with an old index.
func (p *BleveRecallProjection) NewGeneration(id string) (*BleveGeneration, error) {
	if p == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "bleve projection is unavailable", nil)
	}
	if !validBleveGenerationID(id) {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "bleve generation id is invalid", nil)
	}
	p.mu.RLock()
	root := p.root
	p.mu.RUnlock()
	path := filepath.Join(root, bleveGenerationDirectory, id)
	if _, err := os.Stat(path); err == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeConflict, "bleve generation already exists", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect bleve generation: %w", err)
	}
	index, err := bleve.New(path, bleveRecallMapping())
	if err != nil {
		return nil, fmt.Errorf("create bleve generation: %w", err)
	}
	return &BleveGeneration{id: id, path: path, index: index, batch: index.NewBatch()}, nil
}

// Index adds one validated document to a build generation.
func (g *BleveGeneration) Index(ctx context.Context, document sessionmemory.RecallProjectionDocument) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := document.Validate(); err != nil {
		return err
	}
	if g == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "bleve generation is unavailable", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.index == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve generation is closed", nil)
	}
	if g.committed {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "bleve generation was already committed", nil)
	}
	if err := g.batch.Index(document.RevisionID, bleveDocument(document)); err != nil {
		return fmt.Errorf("index bleve recall document: %w", err)
	}
	if g.batch.Size() >= bleveBatchSize {
		if err := g.index.Batch(g.batch); err != nil {
			return fmt.Errorf("commit bleve recall batch: %w", err)
		}
		g.batch.Reset()
	}
	return nil
}

// Delete removes one revision from a build generation before commit.
func (g *BleveGeneration) Delete(ctx context.Context, revisionID string) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(revisionID) == "" {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "bleve revision id is required", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.index == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve generation is closed", nil)
	}
	if g.committed {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "bleve generation was already committed", nil)
	}
	g.batch.Delete(revisionID)
	return nil
}

// Commit durably applies the pending generation batch. Activation is a
// separate operation, so a dirty/partial generation is never advertised.
func (g *BleveGeneration) Commit(ctx context.Context) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if g == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "bleve generation is unavailable", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.index == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve generation is closed", nil)
	}
	if g.committed {
		return nil
	}
	if g.batch.Size() > 0 {
		if err := g.index.Batch(g.batch); err != nil {
			return fmt.Errorf("commit bleve recall generation: %w", err)
		}
		g.batch.Reset()
	}
	if err := os.WriteFile(filepath.Join(g.path, bleveCommittedGenerationFile), []byte(BleveRecallProjectionSchema+"\n"), 0o600); err != nil {
		return fmt.Errorf("write bleve generation commit marker: %w", err)
	}
	g.committed = true
	return nil
}

// Close closes a build generation. It does not activate it.
func (g *BleveGeneration) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.index == nil {
		return nil
	}
	err := g.index.Close()
	g.index = nil
	return err
}

// ActivateGeneration opens a committed generation and atomically switches
// the active marker. Previous generations remain available for rollback and
// are disposable maintenance state.
func (p *BleveRecallProjection) ActivateGeneration(ctx context.Context, id string) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if p == nil || !validBleveGenerationID(id) {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "bleve activation is invalid", nil)
	}
	p.mu.RLock()
	root := p.root
	p.mu.RUnlock()
	path := filepath.Join(root, bleveGenerationDirectory, id)
	marker, markerErr := os.ReadFile(filepath.Join(path, bleveCommittedGenerationFile))
	if markerErr != nil || strings.TrimSpace(string(marker)) != BleveRecallProjectionSchema {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "bleve generation is not durably committed", markerErr)
	}
	index, err := bleve.Open(path)
	if err != nil {
		return fmt.Errorf("open bleve generation for activation: %w", err)
	}
	markerPath := filepath.Join(root, bleveActiveGenerationFile)
	temporaryPath := markerPath + ".tmp"
	if err := os.WriteFile(temporaryPath, []byte(id+"\n"), 0o600); err != nil {
		_ = index.Close()
		return fmt.Errorf("write bleve active marker: %w", err)
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		_ = os.Remove(temporaryPath)
		_ = index.Close()
		return fmt.Errorf("activate bleve generation: %w", err)
	}
	p.mu.Lock()
	previous := p.active
	p.active = index
	p.activeID = id
	p.mu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

// SearchRecall queries only the active generation and returns bounded
// metadata. Canonical hydration and fail-closed validation happen elsewhere.
func (p *BleveRecallProjection) SearchRecall(ctx context.Context, request sessionmemory.RecallRequest) ([]sessionmemory.RecallProjectionHit, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	normalized, err := sessionmemory.NormalizeRecallRequest(request)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "bleve projection is unavailable", nil)
	}
	p.mu.RLock()
	index := p.active
	p.mu.RUnlock()
	if index == nil {
		return []sessionmemory.RecallProjectionHit{}, nil
	}
	textQueries := make([]query.Query, 0, 3)
	for _, field := range []string{bleveFieldTextStandard, bleveFieldTextRussian, bleveFieldTextEnglish} {
		textQuery := bleve.NewMatchQuery(normalized.Query)
		textQuery.SetField(field)
		textQueries = append(textQueries, textQuery)
	}
	clauses := []query.Query{bleve.NewDisjunctionQuery(textQueries...)}
	for _, filter := range []struct {
		value string
		field string
	}{
		{value: normalized.Scope.Key, field: bleveFieldScopeKey},
		{value: string(normalized.Scope.Kind), field: bleveFieldScopeKind},
	} {
		term := bleve.NewTermQuery(filter.value)
		term.SetField(filter.field)
		clauses = append(clauses, term)
	}
	if normalized.Kind != nil {
		term := bleve.NewTermQuery(string(*normalized.Kind))
		term.SetField(bleveFieldKind)
		clauses = append(clauses, term)
	}
	if normalized.Category != nil {
		term := bleve.NewTermQuery(string(*normalized.Category))
		term.SetField(bleveFieldCategory)
		clauses = append(clauses, term)
	}
	if normalized.MemoryKey != "" {
		term := bleve.NewTermQuery(string(normalized.MemoryKey))
		term.SetField(bleveFieldMemoryKey)
		clauses = append(clauses, term)
	}
	if normalized.SourceID != "" {
		term := bleve.NewTermQuery(normalized.SourceID)
		term.SetField(bleveFieldSourceIDs)
		clauses = append(clauses, term)
	}
	if normalized.SessionID != "" {
		term := bleve.NewTermQuery(normalized.SessionID)
		term.SetField(bleveFieldSessionIDs)
		clauses = append(clauses, term)
	}
	candidateLimit := normalized.Limit * 4
	if candidateLimit > sessionmemory.MaxRecallCandidates {
		candidateLimit = sessionmemory.MaxRecallCandidates
	}
	searchRequest := bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(clauses...), candidateLimit, 0, false)
	searchRequest.Fields = []string{bleveFieldScopeKey, bleveFieldScopeKind, bleveFieldItemID, bleveFieldRevisionID, bleveFieldRevision, bleveFieldChangeSeq}
	result, err := index.SearchInContext(ctx, searchRequest)
	if err != nil {
		return nil, fmt.Errorf("search bleve recall projection: %w", err)
	}
	hits := make([]sessionmemory.RecallProjectionHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		metadata, err := storedBleveMetadata(hit.Fields)
		if err != nil {
			return nil, err
		}
		hits = append(hits, sessionmemory.RecallProjectionHit{
			Scope:          metadata.Scope,
			ItemID:         metadata.ItemID,
			RevisionID:     metadata.RevisionID,
			Revision:       metadata.Revision,
			Score:          hit.Score,
			ScopeChangeSeq: metadata.ScopeChangeSeq,
		})
	}
	return hits, nil
}

// Close releases the active index.
func (p *BleveRecallProjection) Close() error {
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

func bleveDocument(document sessionmemory.RecallProjectionDocument) bleveRecallDocument {
	category := ""
	if document.Category != nil {
		category = string(*document.Category)
	}
	return bleveRecallDocument{
		SchemaVersion:  BleveRecallProjectionSchema,
		ScopeKey:       document.Scope.Key,
		ScopeKind:      string(document.Scope.Kind),
		ItemID:         document.ItemID,
		RevisionID:     document.RevisionID,
		Revision:       fmt.Sprintf("%d", document.Revision),
		Kind:           string(document.Kind),
		Category:       category,
		MemoryKey:      string(document.MemoryKey),
		TextStandard:   document.Text,
		TextRussian:    document.Text,
		TextEnglish:    document.Text,
		CreatedAt:      document.CreatedAt.UTC().Format(time.RFC3339Nano),
		SourceIDs:      append([]string(nil), document.SourceIDs...),
		SessionIDs:     append([]string(nil), document.SessionIDs...),
		ScopeChangeSeq: fmt.Sprintf("%d", document.ScopeChangeSeq),
	}
}

func storedBleveMetadata(fields map[string]interface{}) (struct {
	Scope          sessionmemory.Scope
	ItemID         string
	RevisionID     string
	Revision       uint64
	ScopeChangeSeq uint64
}, error) {
	value := func(name string) (string, error) {
		field, ok := fields[name]
		if !ok {
			return "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve hit metadata is incomplete", nil)
		}
		text, ok := field.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve hit metadata has an invalid value", nil)
		}
		return text, nil
	}
	scopeKey, err := value(bleveFieldScopeKey)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	scopeKind, err := value(bleveFieldScopeKind)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	itemID, err := value(bleveFieldItemID)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	revisionID, err := value(bleveFieldRevisionID)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	revisionText, err := value(bleveFieldRevision)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	changeText, err := value(bleveFieldChangeSeq)
	if err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	var revision, change uint64
	if _, err := fmt.Sscan(revisionText, &revision); err != nil || revision == 0 {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve revision metadata is invalid", err)
	}
	if _, err := fmt.Sscan(changeText, &change); err != nil || change == 0 {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve change metadata is invalid", err)
	}
	scope := sessionmemory.Scope{Key: scopeKey, Kind: sessionmemory.ScopeKind(scopeKind)}
	if err := scope.Validate(); err != nil {
		return struct {
			Scope          sessionmemory.Scope
			ItemID         string
			RevisionID     string
			Revision       uint64
			ScopeChangeSeq uint64
		}{}, err
	}
	return struct {
		Scope          sessionmemory.Scope
		ItemID         string
		RevisionID     string
		Revision       uint64
		ScopeChangeSeq uint64
	}{Scope: scope, ItemID: itemID, RevisionID: revisionID, Revision: revision, ScopeChangeSeq: change}, nil
}

func bleveRecallMapping() mapping.IndexMapping {
	mapping := bleve.NewIndexMapping()
	documentMapping := bleve.NewDocumentStaticMapping()
	for _, field := range []string{bleveFieldScopeKey, bleveFieldScopeKind, bleveFieldItemID, bleveFieldRevisionID, bleveFieldRevision, bleveFieldCategory, bleveFieldMemoryKey, bleveFieldCreatedAt, bleveFieldSourceIDs, bleveFieldSessionIDs, bleveFieldChangeSeq} {
		documentMapping.AddFieldMappingsAt(field, bleve.NewKeywordFieldMapping())
	}
	standard := bleve.NewTextFieldMapping()
	standard.Store = false
	standard.Analyzer = "standard"
	documentMapping.AddFieldMappingsAt(bleveFieldTextStandard, standard)
	russian := bleve.NewTextFieldMapping()
	russian.Store = false
	russian.Analyzer = "ru"
	documentMapping.AddFieldMappingsAt(bleveFieldTextRussian, russian)
	english := bleve.NewTextFieldMapping()
	english.Store = false
	english.Analyzer = "en"
	documentMapping.AddFieldMappingsAt(bleveFieldTextEnglish, english)
	mapping.DefaultMapping = documentMapping
	return mapping
}

func validBleveGenerationID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\t")
}
