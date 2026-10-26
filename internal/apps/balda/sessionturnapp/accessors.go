package sessionturnapp

import (
	"context"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/memory"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
)

type sessionAccessor struct {
	manager *baldasession.Manager
}

// activeSession keeps the turn package on its narrow port while adapting the
// concrete session-owned lease API.
type activeSession struct {
	topic *baldasession.TopicSession
}

func wrapActiveSession(topic *baldasession.TopicSession) sessionturn.ActiveSession {
	if topic == nil {
		return nil
	}
	return activeSession{topic: topic}
}

func (s activeSession) AcquireRuntime() (sessionturn.RuntimeLease, error) {
	return s.topic.AcquireRuntime()
}

func (s activeSession) GetSessionID() string { return s.topic.GetSessionID() }

func (s activeSession) GetWorkspaceDir() string { return s.topic.GetWorkspaceDir() }

func NewSessionAccessor(manager *baldasession.Manager) sessionturn.SessionAccessor {
	return sessionAccessor{manager: manager}
}

func (a sessionAccessor) GetSession(locator sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	topic, err := a.manager.GetSession(baldasession.SessionLocator{
		SessionID:   locator.SessionID,
		ChannelType: locator.ChannelType,
		AddressKey:  locator.AddressKey,
		AddressJSON: locator.AddressJSON,
	})
	return wrapActiveSession(topic), err
}

func (a sessionAccessor) RestoreSession(ctx context.Context, sessionCtx sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	ts, err := a.manager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: baldasession.SessionLocator{
			SessionID:   sessionCtx.Locator.SessionID,
			ChannelType: sessionCtx.Locator.ChannelType,
			AddressKey:  sessionCtx.Locator.AddressKey,
			AddressJSON: sessionCtx.Locator.AddressJSON,
		},
		UserID:     sessionCtx.UserID,
		Kind:       baldasession.Kind(sessionCtx.Kind),
		OwnerJobID: sessionCtx.OwnerJobID,
	})
	if errors.Is(err, baldasession.ErrNoPersistedSession) {
		return wrapActiveSession(ts), errors.Join(sessionturn.ErrNoPersistedSession, err)
	}
	return wrapActiveSession(ts), err
}

func (a sessionAccessor) EnsureSession(ctx context.Context, sessionCtx sessionturn.SessionContext, agentName string) (sessionturn.ActiveSession, error) {
	topic, err := a.manager.EnsureSession(ctx, baldasession.SessionContext{
		Locator: baldasession.SessionLocator{
			SessionID:   sessionCtx.Locator.SessionID,
			ChannelType: sessionCtx.Locator.ChannelType,
			AddressKey:  sessionCtx.Locator.AddressKey,
			AddressJSON: sessionCtx.Locator.AddressJSON,
		},
		UserID:     sessionCtx.UserID,
		Kind:       baldasession.Kind(sessionCtx.Kind),
		OwnerJobID: sessionCtx.OwnerJobID,
	}, agentName)
	return wrapActiveSession(topic), err
}

func (a sessionAccessor) EnsureTransientSession(ctx context.Context, sessionCtx sessionturn.SessionContext, agentName string) (sessionturn.ActiveSession, error) {
	topic, err := a.manager.EnsureTransientSession(ctx, baldasession.SessionContext{
		Locator: baldasession.SessionLocator{
			SessionID: sessionCtx.Locator.SessionID, ChannelType: sessionCtx.Locator.ChannelType,
			AddressKey: sessionCtx.Locator.AddressKey, AddressJSON: sessionCtx.Locator.AddressJSON,
		},
		UserID:     sessionCtx.UserID,
		Kind:       baldasession.Kind(sessionCtx.Kind),
		OwnerJobID: sessionCtx.OwnerJobID,
	}, agentName)
	return wrapActiveSession(topic), err
}

type memoryProvider struct {
	store *memory.Store
}

func NewMemoryStateProvider(store *memory.Store) sessionturn.MemoryStateProvider {
	return memoryProvider{store: store}
}

func (p memoryProvider) Enabled() bool {
	return p.store != nil && p.store.MemoryEnabled()
}

func (p memoryProvider) Snapshot(ctx context.Context) (sessionturn.MemorySnapshot, error) {
	snapshot, err := p.store.Snapshot(ctx)
	if err != nil {
		return sessionturn.MemorySnapshot{}, err
	}
	return sessionturn.MemorySnapshot{
		Content:   snapshot.Content,
		Version:   snapshot.Version,
		UpdatedAt: snapshot.UpdatedAt,
	}, nil
}
