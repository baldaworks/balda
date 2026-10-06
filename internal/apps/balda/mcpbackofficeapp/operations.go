// Package mcpbackofficeapp adapts MCP policy owners to Backoffice operations.
package mcpbackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

// Operations delegates management and safe current metadata to their owners.
type Operations struct {
	definitions *mcpmanage.Definitions
	catalog     *catalogapp.Runtime
}

// New composes existing definition and catalog services without additional policy.
func New(definitions *mcpmanage.Definitions, catalog *catalogapp.Runtime) *Operations {
	return &Operations{definitions: definitions, catalog: catalog}
}

// Inventory combines durable definitions with safe current recovery/grant reads.
func (o *Operations) Inventory(ctx context.Context) ([]mcpcmd.Item, error) {
	items, err := o.definitions.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Authorization, err = o.definitions.WorkerAuthorization(ctx, items[i])
		if err != nil {
			return nil, err
		}
		items[i].Recovery, err = o.catalog.CurrentMCPRecovery(ctx, items[i])
		if err != nil {
			return nil, err
		}
	}
	return items, nil
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
