// Package memorybackofficeapp binds the global fact owner to Backoffice's port.
package memorybackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
)

// Operations delegates fact management to the host's shared memory owner.
type Operations struct{ service *memory.Management }

// New constructs the consuming-port adapter without creating another Store.
func New(service *memory.Management) *Operations { return &Operations{service: service} }

// Inventory returns the authorized global fact inventory.
func (o *Operations) Inventory(ctx context.Context, q string, a memorycmd.Authority) (memorycmd.Inventory, error) {
	return o.service.Inventory(ctx, q, a)
}

// Get returns a fact editor at the current global version.
func (o *Operations) Get(ctx context.Context, id int64, a memorycmd.Authority) (memorycmd.Editor, error) {
	return o.service.Get(ctx, id, a)
}

// Create appends an explicitly submitted fact.
func (o *Operations) Create(ctx context.Context, r memorycmd.Create) (memorycmd.Fact, error) {
	return o.service.Create(ctx, r)
}

// Edit changes the selected current fact version.
func (o *Operations) Edit(ctx context.Context, r memorycmd.Edit) (memorycmd.Fact, error) {
	return o.service.Edit(ctx, r)
}

// Delete removes a selected fact without resetting sessions.
func (o *Operations) Delete(ctx context.Context, r memorycmd.Delete) error {
	return o.service.Delete(ctx, r)
}
