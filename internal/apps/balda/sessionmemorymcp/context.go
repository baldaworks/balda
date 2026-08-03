package sessionmemorymcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/sessionmemory"
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
