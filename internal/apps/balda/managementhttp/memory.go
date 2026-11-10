package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListMemoryFacts(ctx context.Context, request managementv1.ListMemoryFactsRequestObject) (managementv1.ListMemoryFactsResponseObject, error) {
	query := ""
	if request.Params.Q != nil {
		query = *request.Params.Q
	}
	inventory, err := s.memory.Inventory(ctx, query, memoryAuthority(ctx))
	if err != nil {
		return nil, err
	}
	facts := make([]managementv1.MemoryFact, 0, len(inventory.Facts))
	for _, fact := range inventory.Facts {
		facts = append(facts, memoryFactDTO(fact))
	}
	return managementv1.ListMemoryFacts200JSONResponse{Enabled: inventory.Enabled, Version: inventory.Version, UpdatedAt: inventory.UpdatedAt, TotalFacts: inventory.TotalFacts, Facts: facts}, nil
}

func (s strictServer) GetMemoryFact(ctx context.Context, request managementv1.GetMemoryFactRequestObject) (managementv1.GetMemoryFactResponseObject, error) {
	editor, err := s.memory.Get(ctx, request.FactID, memoryAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetMemoryFact200JSONResponse{Fact: memoryFactDTO(editor.Fact), ExpectedVersion: editor.ExpectedVersion}, nil
}

func (s strictServer) CreateMemoryFact(ctx context.Context, request managementv1.CreateMemoryFactRequestObject) (managementv1.CreateMemoryFactResponseObject, error) {
	if request.Body == nil {
		return nil, memorycmd.ErrInvalid
	}
	fact, err := s.memory.Create(ctx, memorycmd.Create{Fact: request.Body.Fact, Authority: memoryAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateMemoryFact200JSONResponse(memoryFactDTO(fact)), nil
}

func (s strictServer) UpdateMemoryFact(ctx context.Context, request managementv1.UpdateMemoryFactRequestObject) (managementv1.UpdateMemoryFactResponseObject, error) {
	if request.Body == nil {
		return nil, memorycmd.ErrInvalid
	}
	fact, err := s.memory.Edit(ctx, memorycmd.Edit{ID: request.FactID, Fact: request.Body.Fact, ExpectedVersion: request.Body.ExpectedVersion, Authority: memoryAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateMemoryFact200JSONResponse(memoryFactDTO(fact)), nil
}

func (s strictServer) DeleteMemoryFact(ctx context.Context, request managementv1.DeleteMemoryFactRequestObject) (managementv1.DeleteMemoryFactResponseObject, error) {
	if request.Body == nil || !request.Body.Confirm {
		return nil, memorycmd.ErrInvalid
	}
	if err := s.memory.Delete(ctx, memorycmd.Delete{ID: request.FactID, ExpectedVersion: request.Body.ExpectedVersion, Authority: memoryAuthority(ctx)}); err != nil {
		return nil, err
	}
	return managementv1.DeleteMemoryFact204Response{}, nil
}

func memoryFactDTO(fact memorycmd.Fact) managementv1.MemoryFact {
	return managementv1.MemoryFact{ID: fact.ID, Fact: fact.Fact, CreatedAt: fact.CreatedAt}
}

func memoryAuthority(ctx context.Context) memorycmd.Authority {
	p := webAuthFromContext(ctx).principal
	return memorycmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}
