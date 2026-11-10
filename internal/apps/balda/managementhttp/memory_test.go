package managementhttp

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestMemoryFactHandlersPreserveVersionAndConfirmation(t *testing.T) {
	fixture := &memoryFixture{inventory: memorycmd.Inventory{Enabled: true, Version: 4, UpdatedAt: "now", TotalFacts: 1, Facts: []memorycmd.Fact{{ID: 2, Fact: "alpha", CreatedAt: "then"}}}}
	server := strictServer{memory: fixture}

	listed, err := server.ListMemoryFacts(t.Context(), managementv1.ListMemoryFactsRequestObject{})
	require.NoError(t, err)
	inventory := listed.(managementv1.ListMemoryFacts200JSONResponse)
	require.Equal(t, int64(4), inventory.Version)
	require.Equal(t, int64(2), inventory.Facts[0].ID)

	_, err = server.UpdateMemoryFact(t.Context(), managementv1.UpdateMemoryFactRequestObject{FactID: 2, Body: &managementv1.MemoryFactUpdateRequest{Fact: "beta", ExpectedVersion: 4}})
	require.NoError(t, err)
	require.Equal(t, int64(4), fixture.edited.ExpectedVersion)

	_, err = server.DeleteMemoryFact(t.Context(), managementv1.DeleteMemoryFactRequestObject{FactID: 2, Body: &managementv1.MemoryFactDeleteRequest{ExpectedVersion: 5}})
	require.ErrorIs(t, err, memorycmd.ErrInvalid)
	require.Zero(t, fixture.deleted.ID)
	_, err = server.DeleteMemoryFact(t.Context(), managementv1.DeleteMemoryFactRequestObject{FactID: 2, Body: &managementv1.MemoryFactDeleteRequest{ExpectedVersion: 5, Confirm: true}})
	require.NoError(t, err)
	require.Equal(t, int64(5), fixture.deleted.ExpectedVersion)
}

type memoryFixture struct {
	inventory memorycmd.Inventory
	edited    memorycmd.Edit
	deleted   memorycmd.Delete
}

func (f *memoryFixture) Inventory(context.Context, string, memorycmd.Authority) (memorycmd.Inventory, error) {
	return f.inventory, nil
}
func (f *memoryFixture) Get(context.Context, int64, memorycmd.Authority) (memorycmd.Editor, error) {
	return memorycmd.Editor{}, nil
}
func (f *memoryFixture) Create(context.Context, memorycmd.Create) (memorycmd.Fact, error) {
	return memorycmd.Fact{ID: 1, Fact: "created"}, nil
}
func (f *memoryFixture) Edit(_ context.Context, request memorycmd.Edit) (memorycmd.Fact, error) {
	f.edited = request
	return memorycmd.Fact{ID: request.ID, Fact: request.Fact}, nil
}
func (f *memoryFixture) Delete(_ context.Context, request memorycmd.Delete) error {
	f.deleted = request
	return nil
}

var _ memoryOperations = (*memoryFixture)(nil)
