// Package mcpbackofficeapp adapts MCP policy owners to Backoffice operations.
package mcpbackofficeapp

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

// Operations delegates management and safe current metadata to their owners.
type Operations struct {
	definitions    *mcpmanage.Definitions
	catalog        *catalogapp.Runtime
	authorizations *mcpmanage.Authorizations
}

// New composes existing definition and catalog services without additional policy.
func New(definitions *mcpmanage.Definitions, catalog *catalogapp.Runtime) *Operations {
	return &Operations{definitions: definitions, catalog: catalog}
}

// Inventory combines durable definitions with safe current recovery/grant reads.
func (o *Operations) Inventory(ctx context.Context) ([]mcpcmd.Item, error) {
	return o.InventoryByScope(ctx, mcpcmd.InventoryActive)
}

// InventoryByScope returns the requested lifecycle projection with safe
// current recovery and authorization metadata for active rows.
func (o *Operations) InventoryByScope(ctx context.Context, scope mcpcmd.InventoryScope) ([]mcpcmd.Item, error) {
	items, err := o.definitions.InventoryByScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := o.decorate(ctx, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// Get returns one passive detail projection, including managed tombstones.
func (o *Operations) Get(ctx context.Context, connectionID string) (mcpcmd.Item, error) {
	if strings.HasPrefix(connectionID, "config:") {
		items, err := o.InventoryByScope(ctx, mcpcmd.InventoryActive)
		if err != nil {
			return mcpcmd.Item{}, err
		}
		for _, item := range items {
			if item.Connection.ID == connectionID {
				return item, nil
			}
		}
		return mcpcmd.Item{}, mcpcmd.ErrNotFound
	}
	item, err := o.definitions.Detail(ctx, connectionID)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	if err := o.decorate(ctx, &item); err != nil {
		return mcpcmd.Item{}, err
	}
	return item, nil
}

func (o *Operations) decorate(ctx context.Context, item *mcpcmd.Item) error {
	if item.Connection.Deleted {
		return nil
	}
	var err error
	item.Authorization, err = o.definitions.WorkerAuthorization(ctx, *item)
	if err != nil {
		return err
	}
	item.Recovery, err = o.catalog.CurrentMCPRecovery(ctx, *item)
	return err
}

// ProviderIDs returns configured provider choices.
func (o *Operations) ProviderIDs(ctx context.Context) ([]string, error) {
	return o.definitions.ProviderIDs(ctx)
}

// Create delegates a new managed definition to its owner.
func (o *Operations) Create(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	return o.definitions.Create(ctx, request)
}

// Update delegates an immutable revision edit to its owner.
func (o *Operations) Update(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	return o.definitions.Update(ctx, request)
}

// SetEnabled delegates durable selection to its owner.
func (o *Operations) SetEnabled(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return o.definitions.SetEnabled(ctx, request)
}

// Delete delegates a managed tombstone to its owner.
func (o *Operations) Delete(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error) {
	return o.definitions.Delete(ctx, request)
}

// Probe checks a new candidate without publishing or saving it.
func (o *Operations) Probe(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error) {
	return o.definitions.Probe(ctx, request)
}

// ProbeUpdate checks protected edits against an exact retained revision.
func (o *Operations) ProbeUpdate(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error) {
	return o.definitions.ProbeUpdate(ctx, request)
}

// DiagnoseCurrent checks the authoritative current definition without writes.
func (o *Operations) DiagnoseCurrent(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.DiagnosticResult, error) {
	return o.definitions.DiagnoseCurrent(ctx, connectionID, authority)
}
