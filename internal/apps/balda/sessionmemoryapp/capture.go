package sessionmemoryapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/locatorref"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
)

// ExportPublisher is the bounded local handoff used by turn capture. The
// implementation may be JetStream or another durable transport, but capture
// never calls the remote memory provider directly.
type ExportPublisher interface {
	Publish(ctx context.Context, export sessionmemorycmd.Export) error
}

// ScopeClassifier is owned by a concrete transport codec and wired by the
// composition root. It must be side-effect free and fail closed for malformed
// or ambiguous locator addresses.
type ScopeClassifier func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error)

// ScopeResolver maps canonical locators to exact session-memory partitions.
// The locator ref is the isolation key; the classified kind is metadata.
type ScopeResolver struct {
	classifiers map[string]ScopeClassifier
}

// NewScopeResolver creates a resolver with a defensive copy of classifiers.
func NewScopeResolver(classifiers map[string]ScopeClassifier) ScopeResolver {
	copyOfClassifiers := make(map[string]ScopeClassifier, len(classifiers))
	for channelType, classifier := range classifiers {
		channelType = strings.ToLower(strings.TrimSpace(channelType))
		if channelType == "" || classifier == nil {
			continue
		}
		copyOfClassifiers[channelType] = classifier
	}
	return ScopeResolver{classifiers: copyOfClassifiers}
}

// Resolve validates a canonical locator and classifies its exact scope.
func (r ScopeResolver) Resolve(locator deliverycmd.Locator) (sessionmemory.Scope, error) {
	channelType := strings.TrimSpace(locator.ChannelType)
	addressKey := strings.TrimSpace(locator.AddressKey)
	if channelType == "" || channelType != strings.ToLower(channelType) ||
		addressKey == "" || strings.TrimSpace(locator.AddressJSON) == "" ||
		strings.TrimSpace(locator.SessionID) == "" {
		return sessionmemory.Scope{}, sessionmemory.PermanentError(
			sessionmemory.CodeInvalidScope,
			"locator must contain canonical channel, address, address payload, and session identity",
			nil,
		)
	}
	key := locatorref.Format(locator)
	if key == "" || strings.ContainsAny(key, "\r\n\t") {
		return sessionmemory.Scope{}, sessionmemory.PermanentError(
			sessionmemory.CodeInvalidScope,
			"locator does not have a canonical scope key",
			nil,
		)
	}
	classifier := r.classifiers[channelType]
	if classifier == nil {
		return sessionmemory.Scope{}, sessionmemory.PermanentError(
			sessionmemory.CodeUnsupportedScope,
			fmt.Sprintf("no session-memory classifier is registered for %q", channelType),
			nil,
		)
	}
	kind, err := classifier(locator)
	if err != nil {
		return sessionmemory.Scope{}, sessionmemory.PermanentError(
			sessionmemory.CodeUnsupportedScope,
			"locator scope classification failed closed",
			err,
		)
	}
	scopeKind, err := scopeKindFromLocatorKind(kind)
	if err != nil {
		return sessionmemory.Scope{}, err
	}
	scope := sessionmemory.Scope{Key: key, Kind: scopeKind}
	if err := scope.Validate(); err != nil {
		return sessionmemory.Scope{}, err
	}
	return scope, nil
}

func scopeKindFromLocatorKind(kind deliverycmd.LocatorScopeKind) (sessionmemory.ScopeKind, error) {
	switch kind {
	case deliverycmd.LocatorScopePersonal:
		return sessionmemory.ScopeKindPersonal, nil
	case deliverycmd.LocatorScopeGroup:
		return sessionmemory.ScopeKindGroup, nil
	default:
		return "", sessionmemory.PermanentError(
			sessionmemory.CodeUnsupportedScope,
			"locator scope classifier returned an unsupported kind",
			nil,
		)
	}
}

// CaptureRequest contains only text that is eligible for a completed-turn
// export. Attachments, thoughts, tool payloads, and partial provider events do
// not cross this boundary.
type CaptureRequest struct {
	UserText          string
	AssistantText     string
	Locator           deliverycmd.Locator
	SessionID         string
	AgentSessionID    string
	LineageID         string
	PreviousSessionID string
	SourceTurnID      string
	CompletedAt       time.Time
}

// CaptureResult describes whether a durable handoff was attempted.
type CaptureResult struct {
	Attempted bool
	ExportID  string
	Scope     sessionmemory.Scope
}

// TurnCapture normalizes completed-turn data and publishes a neutral export.
type TurnCapture struct {
	publisher ExportPublisher
	resolver  ScopeResolver
	now       func() time.Time
}

// NewTurnCapture creates a completed-turn capture service. A nil publisher is
// a deterministic disabled-mode no-op.
func NewTurnCapture(publisher ExportPublisher, resolver ScopeResolver) *TurnCapture {
	return &TurnCapture{
		publisher: publisher,
		resolver:  resolver,
		now:       time.Now,
	}
}

// Capture validates one eligible turn, derives its stable ExportID, and hands
// the envelope to the durable publisher. It never invokes a memory Provider.
func (c *TurnCapture) Capture(ctx context.Context, req CaptureRequest) (CaptureResult, error) {
	if c == nil || c.publisher == nil {
		return CaptureResult{}, nil
	}
	userText := strings.TrimSpace(req.UserText)
	assistantText := strings.TrimSpace(req.AssistantText)
	if userText == "" || assistantText == "" {
		return CaptureResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return CaptureResult{}, err
	}
	scope, err := c.resolver.Resolve(req.Locator)
	if err != nil {
		return CaptureResult{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Locator.SessionID)
	}
	agentSessionID := strings.TrimSpace(req.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = sessionID
	}
	session := sessionmemory.SessionRef{
		SessionID:         sessionID,
		AgentSessionID:    agentSessionID,
		LineageID:         strings.TrimSpace(req.LineageID),
		PreviousSessionID: strings.TrimSpace(req.PreviousSessionID),
	}
	completedAt := req.CompletedAt
	if completedAt.IsZero() {
		completedAt = c.currentTime()
	}
	completedAt = completedAt.UTC()
	turn, err := sessionmemory.NewTurn(
		scope,
		session,
		strings.TrimSpace(req.SourceTurnID),
		completedAt,
		userText,
		assistantText,
	)
	if err != nil {
		return CaptureResult{}, err
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		return CaptureResult{}, err
	}
	result := CaptureResult{Attempted: true, ExportID: turn.ExportID, Scope: scope}
	if err := c.publisher.Publish(ctx, export); err != nil {
		return result, err
	}
	return result, nil
}

// CaptureCompletedTurn is the error-only form convenient for composition-root
// adapters that expose a consuming-package lifecycle hook.
func (c *TurnCapture) CaptureCompletedTurn(ctx context.Context, req CaptureRequest) error {
	_, err := c.Capture(ctx, req)
	return err
}

func (c *TurnCapture) currentTime() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}
