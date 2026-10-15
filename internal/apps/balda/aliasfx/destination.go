// Package aliasfx wires managed destination policy to host resolution.
package aliasfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/aliases"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// NewService binds managed destination policy to the selected state provider.
func NewService(provider state.Provider) *aliases.Service {
	return aliases.New(provider.Aliases())
}

// NewDestinationResolver composes role, active-session, and managed-name lookups.
func NewDestinationResolver(destStore *auth.DestinationStore, provider state.Provider,
	managed *aliases.Service) envelopetarget.DestinationResolver {
	return auth.NewDestinationResolverWithManagedAliases(destStore, func(ctx context.Context, sessionID string) (envelopetarget.Resolved, bool, error) {
		record, found, err := provider.Sessions().GetBySessionID(ctx, sessionID)
		if err != nil || !found {
			return envelopetarget.Resolved{}, found, err
		}
		if record.Status != "" && record.Status != state.SessionStatusActive {
			return envelopetarget.Resolved{}, false, nil
		}
		locator, err := deliverycmd.NewLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
		if err != nil {
			return envelopetarget.Resolved{}, false, err
		}
		return envelopetarget.Resolved{Locator: locator, Principal: record.UserID}, true, nil
	}, func(ctx context.Context, name string) (deliverycmd.Locator, error) {
		return managed.Resolve(ctx, name)
	})
}
