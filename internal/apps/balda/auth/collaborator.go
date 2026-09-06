package auth

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
)

type Collaborator = authcmd.Collaborator

type collaboratorStore interface {
	AddCollaborator(ctx context.Context, c Collaborator) error
	RemoveCollaborator(ctx context.Context, userID string) error
	GetCollaborator(ctx context.Context, userID string) (*Collaborator, bool, error)
	ListCollaborators(ctx context.Context) ([]Collaborator, error)
}

type CollaboratorStore struct {
	store collaboratorStore
}

func NewCollaboratorStore(store collaboratorStore) *CollaboratorStore {
	if store == nil {
		return nil
	}
	return &CollaboratorStore{store: store}
}

func (s *CollaboratorStore) AddCollaborator(ctx context.Context, c Collaborator) error {
	if s.store == nil {
		return nil
	}
	return s.store.AddCollaborator(ctx, c)
}

func (s *CollaboratorStore) RemoveCollaborator(ctx context.Context, userID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.RemoveCollaborator(ctx, userID)
}

func (s *CollaboratorStore) GetCollaborator(ctx context.Context, userID string) (*Collaborator, bool, error) {
	if s.store == nil {
		return nil, false, nil
	}
	return s.store.GetCollaborator(ctx, userID)
}

func (s *CollaboratorStore) ListCollaborators(ctx context.Context) ([]Collaborator, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListCollaborators(ctx)
}
