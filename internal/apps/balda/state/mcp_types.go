package state

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// MCPMutation commits identity/selection and an optional new revision together.
// Version zero creates a connection; subsequent writes use optimistic concurrency.
type MCPMutation struct {
	Connection      mcpcmd.Connection
	Revision        *mcpcmd.Revision
	ExpectedVersion uint64
	Authority       mcpcmd.Authority
	Audit           usercmd.AuditEvent
}

// MCPStore retains revisions independently of current connection selection.
type MCPStore interface {
	CheckMCPAuthority(ctx context.Context, authority mcpcmd.Authority) error
	SaveMCPConnection(ctx context.Context, mutation MCPMutation) error
	GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error)
	ListMCPConnections(ctx context.Context) ([]mcpcmd.Connection, error)
	GetMCPRevision(ctx context.Context, connectionID, revisionID string) (mcpcmd.Revision, bool, error)
	ListMCPRevisions(ctx context.Context) ([]mcpcmd.Revision, error)
	MarkMCPPublished(ctx context.Context, id string, version uint64) error
	SaveMCPGrant(ctx context.Context, mutation MCPGrantMutation) error
	GetMCPGrant(ctx context.Context, binding mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error)
	ListMCPGrants(ctx context.Context) ([]mcpcmd.Grant, error)
}

// MCPGrantMutation is one fenced worker grant transition. Only background
// renewal omits browser authority; it still requires the exact generation.
type MCPGrantMutation struct {
	Grant              mcpcmd.Grant
	Operation          mcpcmd.GrantOperation
	ExpectedGeneration uint64
	ExpectedRevisionID string
	Authority          *mcpcmd.Authority
	Audit              usercmd.AuditEvent
}
