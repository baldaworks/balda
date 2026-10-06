package mcpmanage

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// configuredAuthorization exposes current file values only to capture policy.
type configuredAuthorization interface {
	MCPAuthorizationDefinition(ctx context.Context, publicID string) (mcpcmd.Definition, mcpcmd.LaunchValues, error)
}

// PrepareAuthorization loads a managed revision or captures a file-owned
// remote definition in protected storage. File transport and targets are never
// taken from browser input, and retained captures remain independent of edits.
func (s *Definitions) PrepareAuthorization(ctx context.Context, request mcpcmd.PrepareAuthorization) (mcpcmd.Revision, error) {
	request.Authority.At = time.Now().UTC()
	if err := s.store.CheckMCPAuthority(ctx, request.Authority); err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if s.credentials.credentials == nil {
		return mcpcmd.Revision{}, mcpcmd.ErrCredentials
	}
	c, found, err := s.store.GetMCPConnection(ctx, request.ConnectionID)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if found && (c.Deleted || (c.Source != mcpcmd.SourceConfig && c.Source != mcpcmd.SourceManaged)) {
		return mcpcmd.Revision{}, mcpcmd.ErrConflict
	}
	var previous *mcpcmd.Revision
	if found {
		r, exists, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
		if err != nil || !exists {
			return mcpcmd.Revision{}, mcpcmd.ErrUnavailable
		}
		previous = &r
		if c.Source == mcpcmd.SourceManaged {
			if !r.Definition.OAuth || r.Definition.Transport == mcpcmd.TransportStdio || (request.Scopes != nil && !slices.Equal(request.Scopes, r.Definition.Scopes)) {
				return mcpcmd.Revision{}, mcpcmd.ErrInvalid
			}
			return r, nil
		}
	} else {
		publicID, ok := strings.CutPrefix(request.ConnectionID, "config:")
		if !ok || !validPublicID(publicID) {
			return mcpcmd.Revision{}, mcpcmd.ErrNotFound
		}
		connections, err := s.store.ListMCPConnections(ctx)
		if err != nil {
			return mcpcmd.Revision{}, safeOperationError(err)
		}
		for _, existing := range connections {
			if existing.PublicID == publicID {
				if existing.Source != mcpcmd.SourceConfig {
					return mcpcmd.Revision{}, mcpcmd.ErrConflict
				}
				request.ConnectionID = existing.ID
				return s.PrepareAuthorization(ctx, request)
			}
		}
		c = mcpcmd.Connection{ID: rand.Text(), PublicID: publicID, Source: mcpcmd.SourceConfig, Enabled: true, CreatedAt: request.Authority.At}
	}
	configured, ok := s.configured.(configuredAuthorization)
	if !ok {
		return mcpcmd.Revision{}, mcpcmd.ErrUnavailable
	}
	d, values, err := configured.MCPAuthorizationDefinition(ctx, c.PublicID)
	if err != nil {
		return mcpcmd.Revision{}, safeOperationError(err)
	}
	if d.ConfigRevision == "" || d.Transport == mcpcmd.TransportStdio || !validRemoteURL(d.URL) {
		return mcpcmd.Revision{}, mcpcmd.ErrInvalid
	}
	for name := range d.Headers {
		if strings.EqualFold(name, "Authorization") {
			return mcpcmd.Revision{}, mcpcmd.ErrConflict
		}
	}
	d.OAuth, d.AuthBinding, d.Scopes = true, nil, append([]string(nil), request.Scopes...)
	if previous != nil {
		if request.Scopes == nil {
			d.Scopes = append([]string(nil), previous.Definition.Scopes...)
		}
		if previous.Definition.ConfigRevision == d.ConfigRevision && previous.Definition.OAuth && slices.Equal(d.Scopes, previous.Definition.Scopes) {
			return *previous, nil
		}
		if previous.Definition.OAuth && previous.Definition.URL == d.URL && previous.Definition.Transport == d.Transport {
			d.AuthBinding = previous.Definition.AuthBinding
		}
	}
	c.CurrentRevisionID = rand.Text()
	if request.Authority.At.After(c.UpdatedAt) {
		c.UpdatedAt = request.Authority.At
	}
	// Capture current values, not previous bindings: a removed file header
	// must disappear from a new revision while old captures retain it.
	r, err := s.credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: c.ID, ID: c.CurrentRevisionID, Definition: d, CreatedAt: c.UpdatedAt}, mcpcmd.ValueEdits{Env: protectedCapture(values.Env), Headers: protectedCapture(values.Headers)})
	if err != nil {
		return mcpcmd.Revision{}, err
	}
	if err := s.credentials.ValidateTransportRevision(r); err != nil {
		return mcpcmd.Revision{}, err
	}
	if _, err := s.save(ctx, c, &r, c.Version, request.Authority); err != nil {
		return mcpcmd.Revision{}, err
	}
	return r, nil
}

func protectedCapture(values map[string]string) map[string]mcpcmd.ValueEdit {
	edits := make(map[string]mcpcmd.ValueEdit, len(values))
	for key, value := range values {
		edits[key] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: value}
	}
	return edits
}
