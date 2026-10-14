package mcpmanage

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

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

	revision, err := s.currentDiagnosticRevision(ctx, connectionID)
	if err != nil {
		return mcpcmd.DiagnosticResult{}, err
	}
	return s.diagnostic.DiagnoseMCP(ctx, revision), nil
}

func (s *Definitions) currentDiagnosticRevision(ctx context.Context, connectionID string) (mcpcmd.Revision, error) {
	connection, found, err := s.store.GetMCPConnection(ctx, connectionID)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if found {
		if connection.Deleted {
			return mcpcmd.Revision{}, mcpcmd.ErrNotFound
		}
		switch connection.Source {
		case mcpcmd.SourceManaged:
			return s.currentManagedDiagnosticRevision(ctx, connection)
		case mcpcmd.SourceConfig:
			return s.currentConfiguredDiagnosticRevision(ctx, connection.ID, connection.PublicID)
		default:
			return mcpcmd.Revision{}, mcpcmd.ErrConflict
		}
	}

	publicID, ok := strings.CutPrefix(connectionID, "config:")
	if !ok || !validPublicID(publicID) {
		return mcpcmd.Revision{}, mcpcmd.ErrNotFound
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

func (s *Definitions) currentConfiguredDiagnosticRevision(ctx context.Context, connectionID, publicID string) (mcpcmd.Revision, error) {
	configured, ok := s.configured.(configuredAuthorization)
	if !ok {
		return mcpcmd.Revision{}, mcpcmd.ErrUnavailable
	}
	definition, values, err := configured.MCPAuthorizationDefinition(ctx, publicID)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if definition.ConfigRevision == "" {
		return mcpcmd.Revision{}, mcpcmd.ErrInvalid
	}
	revision, err := s.credentials.PrepareRevision(nil, mcpcmd.Revision{
		ConnectionID: connectionID,
		ID:           definition.ConfigRevision,
		Definition:   definition,
	}, mcpcmd.ValueEdits{
		Env:     protectedCapture(values.Env),
		Headers: protectedCapture(values.Headers),
	})
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if err := s.credentials.ValidateTransportRevision(revision); err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	return revision, nil
}
