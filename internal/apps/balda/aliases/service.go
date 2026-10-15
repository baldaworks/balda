// Package aliases owns administrator-managed delivery destination policy.
package aliases

import (
	"context"
	"regexp"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

var aliasName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Store is the persistence and authority port consumed by alias policy.
type Store interface {
	CheckAuthority(ctx context.Context, authority aliascmd.Authority) error
	Get(ctx context.Context, name string) (aliascmd.Record, bool, error)
	List(ctx context.Context) ([]aliascmd.Record, error)
	Save(ctx context.Context, mutation aliascmd.Mutation) error
}

// Service validates aliases and mediates guarded management and resolution.
type Service struct{ store Store }

// New creates managed alias policy with its persistence port.
func New(store Store) *Service { return &Service{store: store} }

// List returns all mappings after checking the current administrator authority.
func (s *Service) List(ctx context.Context, a aliascmd.Authority) ([]aliascmd.Record, error) {
	if err := s.store.CheckAuthority(ctx, a); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// Get returns one mapping after checking the current administrator authority.
func (s *Service) Get(ctx context.Context, name string, a aliascmd.Authority) (aliascmd.Record, error) {
	if err := s.store.CheckAuthority(ctx, a); err != nil {
		return aliascmd.Record{}, err
	}
	record, found, err := s.store.Get(ctx, name)
	if err != nil {
		return aliascmd.Record{}, err
	}
	if !found {
		return aliascmd.Record{}, aliascmd.ErrNotFound
	}
	return record, nil
}

// Create saves a new mapping with a version and security audit fence.
func (s *Service) Create(ctx context.Context, request aliascmd.Create) (aliascmd.Record, error) {
	name, err := validName(request.Name)
	if err != nil {
		return aliascmd.Record{}, err
	}
	ref, err := validLocatorRef(request.LocatorRef)
	if err != nil {
		return aliascmd.Record{}, err
	}
	record := aliascmd.Record{Name: name, LocatorRef: ref, Version: 1}
	m := aliascmd.Mutation{Kind: aliascmd.MutationCreate, Record: record,
		Authority: request.Authority, Audit: aliasAudit(name, usercmd.AuditActionAliasCreated, request.Authority)}
	if err := s.store.Save(ctx, m); err != nil {
		return aliascmd.Record{}, err
	}
	stored, found, err := s.store.Get(ctx, name)
	if err != nil {
		return aliascmd.Record{}, err
	}
	if !found {
		return aliascmd.Record{}, aliascmd.ErrUnavailable
	}
	return stored, nil
}

// Retarget replaces a mapping only at the selected version.
func (s *Service) Retarget(ctx context.Context, request aliascmd.Retarget) (aliascmd.Record, error) {
	name, err := validName(request.Name)
	if err != nil || request.ExpectedVersion == 0 {
		return aliascmd.Record{}, aliascmd.ErrInvalid
	}
	ref, err := validLocatorRef(request.LocatorRef)
	if err != nil {
		return aliascmd.Record{}, err
	}
	record := aliascmd.Record{Name: name, LocatorRef: ref, Version: request.ExpectedVersion + 1}
	m := aliascmd.Mutation{Kind: aliascmd.MutationRetarget, Record: record,
		ExpectedVersion: request.ExpectedVersion, Authority: request.Authority,
		Audit: aliasAudit(name, usercmd.AuditActionAliasRetargeted, request.Authority)}
	if err := s.store.Save(ctx, m); err != nil {
		return aliascmd.Record{}, err
	}
	return record, nil
}

// Delete removes the selected mapping version.
func (s *Service) Delete(ctx context.Context, request aliascmd.Delete) error {
	name, err := validName(request.Name)
	if err != nil || request.ExpectedVersion == 0 {
		return aliascmd.ErrInvalid
	}
	m := aliascmd.Mutation{Kind: aliascmd.MutationDelete,
		Record:          aliascmd.Record{Name: name, Version: request.ExpectedVersion + 1},
		ExpectedVersion: request.ExpectedVersion, Authority: request.Authority,
		Audit: aliasAudit(name, usercmd.AuditActionAliasDeleted, request.Authority)}
	return s.store.Save(ctx, m)
}

// Resolve returns the current concrete locator for a managed name.
func (s *Service) Resolve(ctx context.Context, name string) (deliverycmd.Locator, error) {
	canonical, err := validName(name)
	if err != nil {
		return deliverycmd.Locator{}, err
	}
	record, found, err := s.store.Get(ctx, canonical)
	if err != nil {
		return deliverycmd.Locator{}, err
	}
	if !found {
		return deliverycmd.Locator{}, aliascmd.ErrNotFound
	}
	locator, err := locatorref.Parse(record.LocatorRef)
	if err != nil {
		return deliverycmd.Locator{}, aliascmd.ErrUnavailable
	}
	return locator, nil
}

func validName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !aliasName.MatchString(name) || name == deliverycmd.RoleOwner ||
		name == deliverycmd.RoleCollaborator {
		return "", aliascmd.ErrInvalid
	}
	return name, nil
}

func validLocatorRef(raw string) (string, error) {
	locator, err := locatorref.Parse(raw)
	if err != nil {
		return "", aliascmd.ErrInvalid
	}
	ref := locatorref.Format(locator)
	if ref == "" {
		return "", aliascmd.ErrInvalid
	}
	return ref, nil
}

func aliasAudit(name string, action usercmd.AuditAction, a aliascmd.Authority) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: uuid.NewString(), Action: action,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID,
		ActorSessionID: a.SessionID, TargetType: usercmd.AuditTargetAlias,
		TargetID: name, Source: "backoffice", OccurredAt: a.At}
}
