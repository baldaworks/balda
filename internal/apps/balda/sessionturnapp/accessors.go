package sessionturnapp

import (
	"context"
	"errors"

	"github.com/normahq/balda/internal/apps/balda/memory"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionturn"
)

type sessionAccessor struct {
	manager *baldasession.Manager
}

func NewSessionAccessor(manager *baldasession.Manager) sessionturn.SessionAccessor {
	return sessionAccessor{manager: manager}
}

func (a sessionAccessor) GetSession(locator sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	return a.manager.GetSession(baldasession.SessionLocator{
		SessionID:   locator.SessionID,
		ChannelType: locator.ChannelType,
		AddressKey:  locator.AddressKey,
		AddressJSON: locator.AddressJSON,
	})
}

func (a sessionAccessor) RestoreSession(ctx context.Context, sessionCtx sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	ts, err := a.manager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: baldasession.SessionLocator{
			SessionID:   sessionCtx.Locator.SessionID,
			ChannelType: sessionCtx.Locator.ChannelType,
			AddressKey:  sessionCtx.Locator.AddressKey,
			AddressJSON: sessionCtx.Locator.AddressJSON,
		},
		UserID: sessionCtx.UserID,
	})
	if errors.Is(err, baldasession.ErrNoPersistedSession) {
		return ts, errors.Join(sessionturn.ErrNoPersistedSession, err)
	}
	return ts, err
}

func (a sessionAccessor) EnsureSession(ctx context.Context, sessionCtx sessionturn.SessionContext, agentName string) (sessionturn.ActiveSession, error) {
	return a.manager.EnsureSession(ctx, baldasession.SessionContext{
		Locator: baldasession.SessionLocator{
			SessionID:   sessionCtx.Locator.SessionID,
			ChannelType: sessionCtx.Locator.ChannelType,
			AddressKey:  sessionCtx.Locator.AddressKey,
			AddressJSON: sessionCtx.Locator.AddressJSON,
		},
		UserID: sessionCtx.UserID,
	}, agentName)
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
	return sessionturn.MemorySnapshot{Content: snapshot.Content, Version: snapshot.Version}, nil
}
