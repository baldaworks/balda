package backoffice

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// ErrAdministratorBootstrapRequired prevents serving without an active primary administrator.
var ErrAdministratorBootstrapRequired = errors.New("administrator bootstrap is required")

type stateUserStore interface {
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
}

// StateService validates canonical administrator readiness after provider migrations.
type StateService struct {
	users stateUserStore
}

// NewStateService creates Backoffice state lifecycle operations.
func NewStateService(users stateUserStore) (*StateService, error) {
	if users == nil {
		return nil, fmt.Errorf("backoffice user store is required")
	}
	return &StateService{users: users}, nil
}

// ValidateReady rejects missing canonical administrator bootstrap state.
func (s *StateService) ValidateReady(ctx context.Context) error {
	all, err := listStateUsers(ctx, s.users)
	if err != nil {
		return fmt.Errorf("list canonical users: %w", err)
	}
	if len(all) == 0 {
		return ErrAdministratorBootstrapRequired
	}
	selection, err := users.SelectBootstrapUser(all, "")
	if err != nil {
		return errors.Join(ErrAdministratorBootstrapRequired, fmt.Errorf("validate primary administrator: %w", err))
	}
	primary := findBootstrapUser(all, selection.UserID)
	if primary.ID == "" || primary.Credential.State == usercmd.CredentialStateDisabled {
		return ErrAdministratorBootstrapRequired
	}
	return nil
}

func listStateUsers(ctx context.Context, store stateUserStore) ([]usercmd.User, error) {
	var result []usercmd.User
	page := usercmd.PageRequest{Limit: usercmd.MaxPageSize}
	for {
		usersPage, err := store.ListUsers(ctx, page)
		if err != nil {
			return nil, err
		}
		result = append(result, usersPage.Users...)
		if usersPage.NextAfterID == "" {
			return result, nil
		}
		page.AfterID = usersPage.NextAfterID
	}
}
