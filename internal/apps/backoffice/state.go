package backoffice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/usermigration"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const legacyOwnerKey = "owner"

var (
	// ErrUserMigrationRequired prevents canonical runtime use before legacy authorization is converted.
	ErrUserMigrationRequired = errors.New("legacy user migration is required")
	// ErrAdministratorBootstrapRequired prevents serving without an active primary administrator.
	ErrAdministratorBootstrapRequired = errors.New("administrator bootstrap is required")
)

type legacyOwnerReader interface {
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
}

type legacyCollaboratorReader interface {
	ListCollaborators(ctx context.Context) ([]authcmd.Collaborator, error)
}

type stateUserStore interface {
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
	AnyUserMigrationApplied(ctx context.Context) (bool, error)
	UserMigrationApplied(ctx context.Context, sourceFingerprint string) (bool, error)
	ApplyUserMigration(ctx context.Context, migration usercmd.UserMigration) (bool, error)
}

// StateService validates canonical readiness and runs explicit legacy migration.
type StateService struct {
	owner         legacyOwnerReader
	collaborators legacyCollaboratorReader
	users         stateUserStore
}

// NewStateService creates Backoffice state lifecycle operations.
func NewStateService(owner legacyOwnerReader, collaborators legacyCollaboratorReader, users stateUserStore) (*StateService, error) {
	if owner == nil || collaborators == nil || users == nil {
		return nil, fmt.Errorf("backoffice state dependencies are required")
	}
	return &StateService{owner: owner, collaborators: collaborators, users: users}, nil
}

// ValidateReady rejects pending legacy migration and missing canonical bootstrap state.
func (s *StateService) ValidateReady(ctx context.Context) error {
	if err := s.RequireMigrationComplete(ctx); err != nil {
		return err
	}
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

// RequireMigrationComplete rejects legacy authorization state without a committed marker.
func (s *StateService) RequireMigrationComplete(ctx context.Context) error {
	migrated, err := s.users.AnyUserMigrationApplied(ctx)
	if err != nil {
		return fmt.Errorf("check user migration state: %w", err)
	}
	legacyOwner, ownerFound, err := s.readLegacyOwner(ctx)
	if err != nil {
		return err
	}
	collaborators, err := s.collaborators.ListCollaborators(ctx)
	if err != nil {
		return fmt.Errorf("list legacy collaborators: %w", err)
	}
	if !migrated && (ownerFound || legacyOwner != nil || len(collaborators) != 0) {
		return ErrUserMigrationRequired
	}
	return nil
}

// MigrateUsers converts the complete legacy snapshot and writes credentials only to outputPath.
func (s *StateService) MigrateUsers(ctx context.Context, outputPath, primarySubject string) (usermigration.Result, error) {
	owner, found, err := s.readLegacyOwner(ctx)
	if err != nil {
		return usermigration.Result{}, err
	}
	if !found || owner == nil {
		return usermigration.Result{}, fmt.Errorf("legacy owner state was not found")
	}
	collaborators, err := s.collaborators.ListCollaborators(ctx)
	if err != nil {
		return usermigration.Result{}, fmt.Errorf("list legacy collaborators: %w", err)
	}
	migrator, err := usermigration.New(s.users)
	if err != nil {
		return usermigration.Result{}, err
	}
	return migrator.Migrate(ctx, usermigration.Input{
		Owner: owner, Collaborators: collaborators,
		PrimarySubject: strings.TrimSpace(primarySubject), CredentialsOutput: strings.TrimSpace(outputPath),
	})
}

func (s *StateService) readLegacyOwner(ctx context.Context) (*usermigration.LegacyOwner, bool, error) {
	raw, found, err := s.owner.GetJSON(ctx, legacyOwnerKey)
	if err != nil {
		return nil, false, fmt.Errorf("read legacy owner: %w", err)
	}
	if !found || raw == nil {
		return nil, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, false, fmt.Errorf("encode legacy owner: %w", err)
	}
	var owner usermigration.LegacyOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return nil, false, fmt.Errorf("decode legacy owner: %w", err)
	}
	return &owner, true, nil
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
