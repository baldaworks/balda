package mcpmanage

import (
	"context"
	"crypto/rand"
	"errors"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// Mutation is the feature's atomic definition, authority and audit write.
type Mutation struct {
	Connection      mcpcmd.Connection
	Revision        *mcpcmd.Revision
	ExpectedVersion uint64
	Authority       mcpcmd.Authority
	Audit           usercmd.AuditEvent
}

// DefinitionStore owns durable definitions; authority is fenced at commit.
type DefinitionStore interface {
	CheckMCPAuthority(ctx context.Context, authority mcpcmd.Authority) error
	SaveDefinition(ctx context.Context, mutation Mutation) error
	GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error)
	ListMCPConnections(ctx context.Context) ([]mcpcmd.Connection, error)
	GetMCPRevision(ctx context.Context, connectionID, revisionID string) (mcpcmd.Revision, bool, error)
}

// Configured supplies immutable, already-redacted file-owned definitions.
type Configured interface {
	MCPDefinitions(ctx context.Context) ([]mcpcmd.Item, error)
	ProviderIDs(ctx context.Context) ([]string, error)
}

// Catalog serializes commit and whole-catalog publication under its shared
// mutation lock. Health must describe the exact connection version/revision.
type Catalog interface {
	PublishMCP(ctx context.Context, commit func() error) error
	MCPHealth(ctx context.Context, connection mcpcmd.Connection) (mcpcmd.Status, int, error)
}

// Probe checks a candidate without selecting it for provider execution.
type Probe interface {
	ProbeMCP(ctx context.Context, revision mcpcmd.Revision) (int, error)
}

// Definitions owns hybrid definition validation and management.
type Definitions struct {
	credentials *Service
	store       DefinitionStore
	configured  Configured
	catalog     Catalog
	probe       Probe
}

// NewDefinitions binds the feature to the shared store and catalog owners.
func NewDefinitions(credentials *Service, store DefinitionStore, configured Configured, catalog Catalog, probe Probe) (*Definitions, error) {
	if credentials == nil || store == nil || configured == nil || catalog == nil || probe == nil {
		return nil, mcpcmd.ErrInvalid
	}
	return &Definitions{credentials: credentials, store: store, configured: configured, catalog: catalog, probe: probe}, nil
}

// Create validates and persists a new managed connection and revision.
func (s *Definitions) Create(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	if !validPublicID(request.PublicID) {
		return mcpcmd.Item{}, mcpcmd.ErrInvalid
	}
	if err := s.checkConflict(ctx, request.PublicID, ""); err != nil {
		return mcpcmd.Item{}, err
	}
	c := mcpcmd.Connection{ID: rand.Text(), PublicID: request.PublicID, Source: mcpcmd.SourceManaged, CurrentRevisionID: rand.Text(), Enabled: request.Enabled, CreatedAt: request.Authority.At, UpdatedAt: request.Authority.At}
	r, err := s.prepare(ctx, nil, c, request.Definition, request.Values)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	return s.save(ctx, c, &r, 0, request.Authority)
}

func (s *Definitions) prepare(ctx context.Context, previous *mcpcmd.Revision, c mcpcmd.Connection, definition mcpcmd.Definition, edits mcpcmd.ValueEdits) (mcpcmd.Revision, error) {
	// Existing bindings are changed only through explicit value edits. Creation
	// may carry public literal/reference bindings, never a pre-encrypted payload.
	r, err := s.credentials.PrepareRevision(previous, mcpcmd.Revision{ConnectionID: c.ID, ID: c.CurrentRevisionID, Definition: definition, CreatedAt: c.UpdatedAt}, edits)
	if err != nil {
		return mcpcmd.Revision{}, err
	}
	providers, err := s.configured.ProviderIDs(ctx)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if err := s.validateRevision(r, providers); err != nil {
		return mcpcmd.Revision{}, err
	}
	return r, nil
}

func (s *Definitions) checkConflict(ctx context.Context, publicID, except string) error {
	configured, err := s.configured.MCPDefinitions(ctx)
	if err != nil {
		return safeOperationError(err)
	}
	for _, item := range configured {
		if item.Connection.PublicID == publicID {
			return mcpcmd.ErrConflict
		}
	}
	connections, err := s.store.ListMCPConnections(ctx)
	if err != nil {
		return safeOperationError(err)
	}
	for _, c := range connections {
		if c.PublicID == publicID && c.ID != except {
			return mcpcmd.ErrConflict
		}
	}
	return nil
}

func (s *Definitions) save(ctx context.Context, c mcpcmd.Connection, r *mcpcmd.Revision, version uint64, authority mcpcmd.Authority) (mcpcmd.Item, error) {
	audit := usercmd.AuditEvent{ID: rand.Text(), Action: usercmd.AuditActionMCPDefinitionChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.UserID, ActorSessionID: authority.SessionID, TargetType: usercmd.AuditTargetMCP, TargetID: c.ID, Source: "mcpmanage", OccurredAt: authority.At}
	committed := false
	err := s.catalog.PublishMCP(ctx, func() error {
		if committed {
			return mcpcmd.ErrConflict
		}
		err := s.store.SaveDefinition(ctx, Mutation{Connection: c, Revision: r, ExpectedVersion: version, Authority: authority, Audit: audit})
		committed = err == nil
		return err
	})
	if !committed {
		if err == nil {
			err = mcpcmd.ErrUnavailable
		}
		return mcpcmd.Item{}, safeOperationError(err)
	}
	// A failed publish leaves the version marker pending; the durable save is
	// returned explicitly, allowing catalog recovery rather than duplicate writes.
	c, found, readErr := s.store.GetMCPConnection(ctx, c.ID)
	if readErr != nil {
		return mcpcmd.Item{}, safeOperationError(readErr)
	}
	if !found {
		return mcpcmd.Item{}, mcpcmd.ErrNotFound
	}
	item, itemErr := s.item(ctx, c)
	if itemErr == nil && err != nil && item.Status == mcpcmd.StatusReady {
		item.Status = mcpcmd.StatusPending
		item.ToolCount = 0
	}
	return item, itemErr
}

func (s *Definitions) item(ctx context.Context, c mcpcmd.Connection) (mcpcmd.Item, error) {
	r, found, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
	if err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	if !found {
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}
	item := mcpcmd.Item{Connection: c, Definition: r.Definition, Status: mcpcmd.StatusPending}
	switch {
	case c.Deleted:
		item.Status = mcpcmd.StatusDeleted
	case !c.Enabled:
		item.Status = mcpcmd.StatusDisabled
	case c.PublishedVersion != c.Version:
	default:
		status, count, err := s.catalog.MCPHealth(ctx, c)
		if err != nil {
			item.Status = mcpcmd.StatusUnavailable
			break
		}
		if status == mcpcmd.StatusReady && count >= 0 {
			item.Status = status
			item.ToolCount = count
		} else if status == mcpcmd.StatusUnavailable {
			item.Status = status
		}
	}
	return item, nil
}

func safeOperationError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, mcpcmd.ErrInvalid, mcpcmd.ErrConflict, mcpcmd.ErrNotFound, mcpcmd.ErrForbidden, mcpcmd.ErrCredentials, mcpcmd.ErrUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return mcpcmd.ErrUnavailable
}

// Update creates a new immutable revision while preserving public identity.
func (s *Definitions) Update(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	c, err := s.managed(ctx, request.ConnectionID, request.ExpectedVersion)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	if err := s.checkConflict(ctx, c.PublicID, c.ID); err != nil {
		return mcpcmd.Item{}, err
	}
	previous, found, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
	if err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	if !found {
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}
	c.CurrentRevisionID = rand.Text()
	c.Enabled = request.Enabled
	c.UpdatedAt = request.Authority.At
	r, err := s.prepare(ctx, &previous, c, request.Definition, request.Values)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	return s.save(ctx, c, &r, request.ExpectedVersion, request.Authority)
}

// SetEnabled changes new selection without rewriting retained definitions.
func (s *Definitions) SetEnabled(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return s.changeSelection(ctx, request, false)
}

// Delete tombstones an identity; historical revisions and pins remain intact.
func (s *Definitions) Delete(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return s.changeSelection(ctx, request, true)
}

func (s *Definitions) changeSelection(ctx context.Context, request mcpcmd.ChangeSelection, deleted bool) (mcpcmd.Item, error) {
	c, err := s.managed(ctx, request.ConnectionID, request.ExpectedVersion)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	if request.Enabled && !deleted {
		if err := s.checkConflict(ctx, c.PublicID, c.ID); err != nil {
			return mcpcmd.Item{}, err
		}
		r, found, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
		if err != nil {
			return mcpcmd.Item{}, safeOperationError(err)
		}
		if !found {
			return mcpcmd.Item{}, mcpcmd.ErrUnavailable
		}
		providers, err := s.configured.ProviderIDs(ctx)
		if err != nil {
			return mcpcmd.Item{}, safeOperationError(err)
		}
		if err := s.validateRevision(r, providers); err != nil {
			return mcpcmd.Item{}, err
		}
	}
	c.Enabled = request.Enabled && !deleted
	c.Deleted = deleted
	c.UpdatedAt = request.Authority.At
	return s.save(ctx, c, nil, request.ExpectedVersion, request.Authority)
}

func (s *Definitions) managed(ctx context.Context, id string, version uint64) (mcpcmd.Connection, error) {
	c, found, err := s.store.GetMCPConnection(ctx, id)
	if err != nil {
		return mcpcmd.Connection{}, safeOperationError(err)
	}
	if !found {
		configured, err := s.configured.MCPDefinitions(ctx)
		if err != nil {
			return mcpcmd.Connection{}, safeOperationError(err)
		}
		for _, item := range configured {
			if item.Connection.ID == id {
				return mcpcmd.Connection{}, mcpcmd.ErrForbidden
			}
		}
		return mcpcmd.Connection{}, mcpcmd.ErrNotFound
	}
	if c.Source != mcpcmd.SourceManaged {
		return mcpcmd.Connection{}, mcpcmd.ErrForbidden
	}
	if version == 0 || c.Version != version || c.Deleted {
		return mcpcmd.Connection{}, mcpcmd.ErrConflict
	}
	return c, nil
}

// Inventory combines file-owned and managed definitions without precedence.
func (s *Definitions) Inventory(ctx context.Context) ([]mcpcmd.Item, error) {
	configured, err := s.configured.MCPDefinitions(ctx)
	if err != nil {
		return nil, safeOperationError(err)
	}
	connections, err := s.store.ListMCPConnections(ctx)
	if err != nil {
		return nil, safeOperationError(err)
	}
	items := append([]mcpcmd.Item(nil), configured...)
	configuredIndex := make(map[string]int, len(configured))
	for i := range items {
		items[i].Connection.Source = mcpcmd.SourceConfig
		configuredIndex[items[i].Connection.PublicID] = i
		// Config has no durable version marker. Observed health still belongs
		// to the catalog; a file entry alone never establishes readiness.
		items[i].Status = mcpcmd.StatusPending
		items[i].ToolCount = 0
		status, count, err := s.catalog.MCPHealth(ctx, items[i].Connection)
		if err != nil || status == mcpcmd.StatusUnavailable {
			items[i].Status = mcpcmd.StatusUnavailable
		} else if status == mcpcmd.StatusReady && count >= 0 {
			items[i].Status = status
			items[i].ToolCount = count
		}
	}
	for _, c := range connections {
		if c.Source == mcpcmd.SourceConfig {
			// A configured OAuth side record owns identity/auth only. Its
			// retained revision never overrides the current file definition.
			if i, ok := configuredIndex[c.PublicID]; ok {
				items[i].Connection.ID = c.ID
			}
			continue
		}
		item, err := s.item(ctx, c)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	counts := make(map[string]int, len(items))
	for _, item := range items {
		counts[item.Connection.PublicID]++
	}
	for i := range items {
		if counts[items[i].Connection.PublicID] > 1 {
			items[i].Status = mcpcmd.StatusConflict
			items[i].ToolCount = 0
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Connection.PublicID == items[j].Connection.PublicID {
			return items[i].Connection.Source < items[j].Connection.Source
		}
		return items[i].Connection.PublicID < items[j].Connection.PublicID
	})
	return items, nil
}

// Probe validates and discovers a candidate without publishing or saving it.
func (s *Definitions) Probe(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	if err := s.store.CheckMCPAuthority(ctx, request.Authority); err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	c := mcpcmd.Connection{ID: rand.Text(), CurrentRevisionID: rand.Text(), UpdatedAt: request.Authority.At}
	r, err := s.prepare(ctx, nil, c, request.Definition, request.Values)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	item := mcpcmd.Item{Definition: r.Definition, Status: mcpcmd.StatusPending}
	count, err := s.probe.ProbeMCP(ctx, r)
	if err != nil || count < 0 {
		item.Status = mcpcmd.StatusUnavailable
		return item, mcpcmd.ErrUnavailable
	}
	item.ToolCount = count
	return item, nil
}
