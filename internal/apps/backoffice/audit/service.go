// Package audit owns the bounded, read-only Backoffice security-event query.
package audit

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

const (
	defaultPageSize = 25
	maxScanPages    = 5
)

// Store is the Audit-owned persistence query port.
type Store interface {
	ListAuditEvents(ctx context.Context, page usercmd.PageRequest) (usercmd.AuditPage, error)
}

// Request is one bounded stable audit query.
type Request struct {
	AfterID    string
	Limit      int
	Action     usercmd.AuditAction
	Outcome    usercmd.AuditOutcome
	TargetType usercmd.AuditTargetType
}

// Page is one filtered page and its exclusive stable cursor.
type Page struct {
	Events      []usercmd.AuditEvent
	NextAfterID string
}

// Service queries typed security events for current administrators.
type Service struct {
	store Store
}

// NewService creates the read-only Audit use case.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List returns a bounded filtered page without widening the security-event scope.
func (s *Service) List(ctx context.Context, actor usercmd.User, request Request) (Page, error) {
	if !users.BackofficeCapabilities(actor).ViewAudit {
		return Page{}, usercmd.ErrForbidden
	}
	request.AfterID = strings.TrimSpace(request.AfterID)
	if len(request.AfterID) > 256 {
		return Page{}, fmt.Errorf("%w: audit cursor exceeds its bound", usercmd.ErrInvalid)
	}
	if request.Limit == 0 {
		request.Limit = defaultPageSize
	}
	if request.Limit < 1 || request.Limit > usercmd.MaxPageSize ||
		(request.Action != "" && !request.Action.Valid()) ||
		(request.Outcome != "" && !request.Outcome.Valid()) ||
		(request.TargetType != "" && !request.TargetType.Valid()) {
		return Page{}, fmt.Errorf("%w: audit filter or page size is invalid", usercmd.ErrInvalid)
	}

	result := Page{Events: make([]usercmd.AuditEvent, 0, request.Limit)}
	cursor := request.AfterID
	for scanPage := 0; scanPage < maxScanPages; scanPage++ {
		stored, err := s.store.ListAuditEvents(ctx, usercmd.PageRequest{AfterID: cursor, Limit: usercmd.MaxPageSize})
		if err != nil {
			return Page{}, fmt.Errorf("list security audit events: %w", err)
		}
		for index, event := range stored.Events {
			cursor = event.ID
			if !matches(request, event) {
				continue
			}
			result.Events = append(result.Events, event)
			if len(result.Events) == request.Limit {
				if index+1 < len(stored.Events) || stored.NextAfterID != "" {
					result.NextAfterID = cursor
				}
				return result, nil
			}
		}
		if stored.NextAfterID == "" {
			return result, nil
		}
		cursor = stored.NextAfterID
		result.NextAfterID = cursor
	}
	return result, nil
}

func matches(request Request, event usercmd.AuditEvent) bool {
	return (request.Action == "" || event.Action == request.Action) &&
		(request.Outcome == "" || event.Outcome == request.Outcome) &&
		(request.TargetType == "" || event.TargetType == request.TargetType)
}
