package zulip

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
)

// RestartSessionLabel resolves the recreated session label for a Zulip restart flow.
func RestartSessionLabel(isDM bool, info baldasession.TopicSessionInfo, ownerLabel, autoLabel string) string {
	if label := strings.TrimSpace(info.AgentName); label != "" {
		return label
	}
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

// RestartSessionUserID resolves the recreated session user ID for a Zulip restart flow.
func RestartSessionUserID(senderID int, info baldasession.TopicSessionInfo) string {
	if userID := strings.TrimSpace(info.UserID); userID != "" {
		return userID
	}
	return UserID(senderID)
}

// RestartWelcomeDisplayName resolves the display name used in the restart welcome message.
func RestartWelcomeDisplayName(isDM bool, label, ownerLabel string) string {
	if !isDM {
		return ownerLabel
	}
	return label
}

// SessionWelcomeLabel resolves the default welcome label for a Zulip session.
func SessionWelcomeLabel(isDM bool, ownerLabel, autoLabel string) string {
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

// SessionManager is the transport-facing subset of session operations Zulip needs.
type SessionManager interface {
	GetSession(locator baldasession.SessionLocator) (*baldasession.TopicSession, error)
	RestoreSession(ctx context.Context, sessionCtx baldasession.SessionContext) (*baldasession.TopicSession, error)
	EnsureSession(ctx context.Context, sessionCtx baldasession.SessionContext, agentName string) (*baldasession.TopicSession, error)
	GetAgentMetadata(agentName string) baldasession.AgentMetadata
}

// EnsureSession ensures or restores the Zulip session for one inbound conversation.
func EnsureSession(
	ctx context.Context,
	manager SessionManager,
	locator baldasession.SessionLocator,
	transportUserID string,
	providerName string,
	isDM bool,
	ownerLabel string,
	autoLabel string,
) (*baldasession.TopicSession, bool, error) {
	if manager == nil {
		return nil, false, fmt.Errorf("session manager is unavailable")
	}
	existing, _ := manager.GetSession(locator)
	if existing != nil {
		return existing, false, nil
	}
	if strings.TrimSpace(providerName) == "" {
		return nil, false, fmt.Errorf("no provider configured")
	}
	ts, err := manager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  transportUserID,
	})
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, false, err
	}
	if err == nil && ts != nil {
		return ts, true, nil
	}
	label := SessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	ts, err = manager.EnsureSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  transportUserID,
	}, label)
	if err != nil {
		return nil, false, err
	}
	return ts, true, nil
}

// BuildSessionPreparation converts a ready Zulip session into a transport-neutral preparation result.
func BuildSessionPreparation(ts *baldasession.TopicSession, requesterUserID string) ingressapp.SessionPreparation {
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          ts.GetUserID(),
		RequesterUserID: requesterUserID,
		AgentSessionID:  ts.GetAgentSessionID(),
	}
}

// BuildSessionWelcome builds the welcome message sent when Zulip creates or restores a session.
func BuildSessionWelcome(manager SessionManager, providerName string, isDM bool, sessionID string, ownerLabel string, autoLabel string) string {
	label := SessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(label, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

// BuildRestartWelcome builds the restart welcome message for Zulip.
func BuildRestartWelcome(manager SessionManager, providerName string, isDM bool, label string, sessionID string, ownerLabel string) string {
	metadata := manager.GetAgentMetadata(providerName)
	welcomeName := RestartWelcomeDisplayName(isDM, label, ownerLabel)
	return welcome.BuildAgentWelcomeMessage(welcomeName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

// BuildTopicWelcome builds the welcome message for a new Zulip topic session.
func BuildTopicWelcome(manager SessionManager, providerName string, topicName string, sessionID string) string {
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(topicName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

// BuildLocatorMessage builds the human-facing locator description for Zulip.
func BuildLocatorMessage(locator deliverycmd.Locator) string {
	ref := locatorref.Format(locator)
	return fmt.Sprintf(
		"Transport: %s\nLocator: %s\n\nUse in scheduler/webhook config:\ntarget: locator\nkey: %s",
		locator.ChannelType, ref, ref,
	)
}
