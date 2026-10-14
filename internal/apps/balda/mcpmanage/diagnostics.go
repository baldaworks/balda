package mcpmanage

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type transientDiagnosticProbe interface {
	DiagnoseMCPWithValues(ctx context.Context, revision mcpcmd.Revision, values mcpcmd.LaunchValues) mcpcmd.DiagnosticResult
}

type diagnosticTarget struct {
	revision mcpcmd.Revision
	values   *mcpcmd.LaunchValues
}

// DiagnoseCurrent resolves and checks only the authoritative current
// definition identified by connectionID. The diagnostic result is ephemeral.
func (s *Definitions) DiagnoseCurrent(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.DiagnosticResult, error) {
	if err := ctx.Err(); err != nil {
		return mcpcmd.DiagnosticResult{}, err
	}
	authority.At = time.Now().UTC()
	if err := s.store.CheckMCPAuthority(ctx, authority); err != nil {
		return mcpcmd.DiagnosticResult{}, safeOperationError(err)
	}

	target, err := s.currentDiagnosticTarget(ctx, connectionID)
	if err != nil {
		return mcpcmd.DiagnosticResult{}, err
	}
	if target.values == nil {
		return s.diagnostic.DiagnoseMCP(ctx, target.revision), nil
	}
	diagnostic, ok := s.diagnostic.(transientDiagnosticProbe)
	if !ok {
		return mcpcmd.DiagnosticResult{}, mcpcmd.ErrUnavailable
	}
	return diagnostic.DiagnoseMCPWithValues(ctx, target.revision, *target.values), nil
}

func (s *Definitions) currentDiagnosticTarget(ctx context.Context, connectionID string) (diagnosticTarget, error) {
	connection, found, err := s.store.GetMCPConnection(ctx, connectionID)
	if err != nil {
		return diagnosticTarget{}, safeOperationError(err)
	}
	if found {
		if connection.Deleted {
			return diagnosticTarget{}, mcpcmd.ErrNotFound
		}
		switch connection.Source {
		case mcpcmd.SourceManaged:
			revision, err := s.currentManagedDiagnosticRevision(ctx, connection)
			return diagnosticTarget{revision: revision}, err
		case mcpcmd.SourceConfig:
			return s.currentConfiguredDiagnosticRevision(ctx, connection.ID, connection.PublicID)
		default:
			return diagnosticTarget{}, mcpcmd.ErrConflict
		}
	}

	publicID, ok := strings.CutPrefix(connectionID, "config:")
	if !ok || !validPublicID(publicID) {
		return diagnosticTarget{}, mcpcmd.ErrNotFound
	}
	return s.currentConfiguredDiagnosticRevision(ctx, connectionID, publicID)
}

func (s *Definitions) currentManagedDiagnosticRevision(ctx context.Context, connection mcpcmd.Connection) (mcpcmd.Revision, error) {
	if connection.CurrentRevisionID == "" {
		return mcpcmd.Revision{}, mcpcmd.ErrUnavailable
	}
	revision, found, err := s.store.GetMCPRevision(ctx, connection.ID, connection.CurrentRevisionID)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if !found || revision.ConnectionID != connection.ID || revision.ID != connection.CurrentRevisionID {
		return mcpcmd.Revision{}, mcpcmd.ErrUnavailable
	}
	if err := s.credentials.ValidateTransportRevision(revision); err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	return revision, nil
}

func (s *Definitions) currentConfiguredDiagnosticRevision(ctx context.Context, connectionID, publicID string) (diagnosticTarget, error) {
	configured, ok := s.configured.(configuredPrivateDefinition)
	if !ok {
		return diagnosticTarget{}, mcpcmd.ErrUnavailable
	}
	definition, values, err := configured.MCPPrivateDefinition(ctx, publicID)
	if err != nil {
		return diagnosticTarget{}, safeOperationError(err)
	}
	if definition.ConfigRevision == "" {
		return diagnosticTarget{}, mcpcmd.ErrInvalid
	}
	revision := mcpcmd.Revision{
		ConnectionID: connectionID,
		ID:           definition.ConfigRevision,
		Definition:   definition,
	}
	values, err = s.credentials.resolveTransientValues(revision, values)
	if err != nil {
		return diagnosticTarget{}, safeOperationError(err)
	}
	return diagnosticTarget{revision: revision, values: &values}, nil
}
