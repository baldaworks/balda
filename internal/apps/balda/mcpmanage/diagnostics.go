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
// definition identified by connectionID. The last secret-free result is kept
// for the exact revision for the lifetime of this Definitions instance.
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
	var result mcpcmd.DiagnosticResult
	if target.values == nil {
		result = s.diagnostic.DiagnoseMCP(ctx, target.revision)
	} else {
		diagnostic, ok := s.diagnostic.(transientDiagnosticProbe)
		if !ok {
			return mcpcmd.DiagnosticResult{}, mcpcmd.ErrUnavailable
		}
		result = diagnostic.DiagnoseMCPWithValues(ctx, target.revision, *target.values)
	}
	result.CheckedAt = time.Now().UTC()
	s.diagnosticMu.Lock()
	s.diagnostics[connectionID] = diagnosticObservation{revisionID: target.revision.ID, result: result}
	s.diagnosticMu.Unlock()
	return result, nil
}

func (s *Definitions) attachLastDiagnostic(item *mcpcmd.Item) {
	revisionID := item.Connection.CurrentRevisionID
	if item.Connection.Source == mcpcmd.SourceConfig {
		revisionID = item.Definition.ConfigRevision
	}
	s.diagnosticMu.RLock()
	observation, ok := s.diagnostics[item.Connection.ID]
	s.diagnosticMu.RUnlock()
	if !ok || observation.revisionID != revisionID {
		return
	}
	result := observation.result
	item.LastDiagnostic = &result
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
