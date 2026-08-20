package sessionmemorymcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/sessionmemory"
)

const (
	// HeaderSessionLocator is populated by the authenticated Balda runtime
	// adapter. It is deliberately not part of the tool input schema.
	HeaderSessionLocator = "X-Balda-Session-Locator"
	HeaderSessionID      = "X-Balda-Session-ID"
	HeaderAgentSessionID = "X-Balda-Agent-Session-ID"
	HeaderLineageID      = "X-Balda-Session-Lineage-ID"
	HeaderSessionBinding = "X-Balda-Session-Binding"
)

// CurrentSession is the authenticated identity attached to one MCP call.
// The neutral MCP adapter receives only the locator-derived scope; Balda
// keeps the provider session identity here for binding and authentication.
type CurrentSession struct {
	Locator deliverycmd.Locator
	Session sessionmemory.SessionRef
}

// SessionResolver supplies the authenticated current identity out of band.
// Tool arguments are never consulted for scope selection.
type SessionResolver interface {
	Resolve(ctx context.Context, req *mcp.CallToolRequest) (CurrentSession, error)
}

// HeaderSessionResolver is the composition boundary for runtimes that can
// attach the current Balda session to an MCP HTTP request. The ContextBroker
// capability must authenticate the headers; locator/session headers alone are
// never trusted. The bundled server is bound to localhost, and deployments
// exposing it through a proxy must preserve the binding header.
type HeaderSessionResolver struct {
	Broker *ContextBroker
}

func (r HeaderSessionResolver) Resolve(_ context.Context, req *mcp.CallToolRequest) (CurrentSession, error) {
	if req == nil || req.GetExtra() == nil {
		return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "current session context is unavailable", nil)
	}
	headers := req.GetExtra().Header
	if r.Broker == nil || !r.Broker.Verify(headers) {
		return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "current session context is unauthenticated", nil)
	}
	locatorJSON := strings.TrimSpace(headers.Get(HeaderSessionLocator))
	if locatorJSON == "" {
		return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "current session context is unavailable", nil)
	}
	var locator deliverycmd.Locator
	if err := json.Unmarshal([]byte(locatorJSON), &locator); err != nil {
		return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "current session context is invalid", nil)
	}
	sessionID := strings.TrimSpace(headers.Get(HeaderSessionID))
	if sessionID == "" {
		sessionID = strings.TrimSpace(locator.SessionID)
	}
	agentSessionID := strings.TrimSpace(headers.Get(HeaderAgentSessionID))
	if agentSessionID == "" {
		agentSessionID = sessionID
	}
	if sessionID == "" || agentSessionID == "" {
		return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidSession, "current session context is invalid", nil)
	}
	return CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{
			SessionID:      sessionID,
			AgentSessionID: agentSessionID,
			LineageID:      strings.TrimSpace(headers.Get(HeaderLineageID)),
		},
	}, nil
}

var _ SessionResolver = HeaderSessionResolver{}

// ScopeResolverAdapter keeps authenticated Balda context and transport
// locator classification in the host while satisfying the neutral public MCP
// port. Tool arguments are never consulted to resolve the scope.
type ScopeResolverAdapter struct {
	Session SessionResolver
	Locator sessionmemoryapp.ScopeResolver
}

func (a ScopeResolverAdapter) Resolve(ctx context.Context, req *mcp.CallToolRequest) (sessionmemory.Scope, error) {
	if a.Session == nil {
		return sessionmemory.Scope{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidScope, "current session scope is unavailable", nil)
	}
	current, err := a.Session.Resolve(ctx, req)
	if err != nil {
		return sessionmemory.Scope{}, err
	}
	return a.Locator.Resolve(current.Locator)
}
