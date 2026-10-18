package chatfx

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
)

const autoSessionLabel = "auto"

// SessionAdapter adapts baldasession.Manager to chatapp.SessionPreparer.
type SessionAdapter struct {
	sessions    *baldasession.Manager
	directLabel string
}

// NewSessionAdapter constructs a SessionAdapter backed by baldasession.Manager.
func NewSessionAdapter(sessions *baldasession.Manager) *SessionAdapter {
	return &SessionAdapter{sessions: sessions}
}

// Prepare ensures or restores a topic session and returns preparation metadata.
func (a *SessionAdapter) Prepare(ctx context.Context, inbound chatapp.InboundContext) (chatapp.SessionPreparation, error) {
	if a == nil || a.sessions == nil {
		return chatapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session manager is unavailable"))
	}
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	existing, _ := a.sessions.GetSession(locator)
	label := autoSessionLabel
	if inbound.Direct && inbound.TopicID == 0 && a.directLabel != "" {
		label = a.directLabel
	}
	topicSession, err := a.sessions.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: inbound.UserID}, label)
	if err != nil {
		return chatapp.SessionPreparation{}, err
	}
	metadata := a.sessions.GetAgentMetadata(a.sessions.BaldaProviderID())
	return chatapp.SessionPreparation{
		ArchiveID:     topicSession.GetArchiveID(),
		Activated:     existing == nil,
		SessionID:     topicSession.GetSessionID(),
		SessionLabel:  topicSession.GetAgentName(),
		StartupNotice: a.sessions.TakeStartupNotice(topicSession.GetSessionID()),
		AgentType:     metadata.Type, Model: metadata.Model, ReasoningEffort: metadata.ReasoningEffort,
		MCPServers:        metadata.MCPServers,
		Ready:             true,
		UserID:            topicSession.GetUserID(),
		RequesterUserID:   inbound.UserID,
		AgentSessionID:    topicSession.GetAgentSessionID(),
		TopicID:           inbound.TopicID,
		WorkspaceDir:      topicSession.GetWorkspaceDir(),
		RuntimeSnapshotID: topicSession.GetRuntimeSnapshotID(),
	}, nil
}

// IngressSessionAdapter projects the shared preparation into the ingressapp port.
type IngressSessionAdapter struct{ shared *SessionAdapter }

// NewIngressSessionAdapter uses the same session preparation as chatapp, with
// the existing main-DM label default supplied at this composition boundary.
func NewIngressSessionAdapter(sessions *baldasession.Manager) ingressapp.SessionPreparer {
	return &IngressSessionAdapter{shared: &SessionAdapter{sessions: sessions, directLabel: "balda"}}
}

// Prepare delegates lifecycle preparation and only converts neutral port values.
func (a *IngressSessionAdapter) Prepare(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	prep, err := a.shared.Prepare(ctx, chatapp.InboundContext{
		InboundID: inbound.InboundID, SessionID: inbound.SessionID, ChannelType: inbound.ChannelType,
		AddressKey: inbound.AddressKey, AddressJSON: inbound.AddressJSON, UserID: inbound.UserID,
		TopicID: inbound.TopicID, Direct: inbound.Direct, Source: inbound.Source,
	})
	return ingressapp.SessionPreparation{
		ArchiveID: prep.ArchiveID,
		Activated: prep.Activated, SessionID: prep.SessionID, SessionLabel: prep.SessionLabel,
		StartupNotice: prep.StartupNotice, AgentType: prep.AgentType, Model: prep.Model,
		ReasoningEffort: prep.ReasoningEffort, MCPServers: prep.MCPServers,
		Ready: prep.Ready, Reason: prep.Reason, UserID: prep.UserID, RequesterUserID: prep.RequesterUserID,
		AgentSessionID: prep.AgentSessionID, TopicID: prep.TopicID, WorkspaceDir: prep.WorkspaceDir,
		RuntimeSnapshotID: prep.RuntimeSnapshotID,
	}, err
}
