package channel

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

// Router routes outbound channel operations to the correct ChannelAdapter
// based on the locator's ChannelType.
type Router struct {
	adapters map[string]ChannelAdapter
}

func NewRouter(adapters map[string]ChannelAdapter) *Router { return &Router{adapters: adapters} }

func (r *Router) adapterFor(locator baldasession.SessionLocator) (ChannelAdapter, error) {
	adapter, ok := r.adapters[locator.ChannelType]
	if !ok {
		return nil, fmt.Errorf("no channel adapter for channel type %q", locator.ChannelType)
	}
	return adapter, nil
}

func (r *Router) SendPlain(ctx context.Context, locator baldasession.SessionLocator, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendPlain(ctx, locator, text)
}

func (r *Router) SendMarkdown(ctx context.Context, locator baldasession.SessionLocator, text string) error {
	return r.SendMarkdownWithProfile(ctx, locator, deliverycmd.Profile{}, text)
}

func (r *Router) SendMarkdownWithProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendMarkdownWithProfile(ctx, locator, profile, text)
}

func (r *Router) SendAgentReply(ctx context.Context, locator baldasession.SessionLocator, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendAgentReply(ctx, locator, text)
}

func (r *Router) SendAgentReplyWithProviderMessageID(ctx context.Context, locator baldasession.SessionLocator, text string) (string, error) {
	return r.SendAgentReplyWithProviderMessageIDAndProfile(ctx, locator, deliverycmd.Profile{}, text)
}

func (r *Router) SendAgentReplyWithProviderMessageIDAndProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) (string, error) {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return "", err
	}
	return adapter.SendAgentReplyWithProviderMessageIDAndProfile(ctx, locator, profile, text)
}

func (r *Router) SendDraftPlain(ctx context.Context, locator baldasession.SessionLocator, draftID int, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendDraftPlain(ctx, locator, draftID, text)
}

func (r *Router) SendTyping(ctx context.Context, locator baldasession.SessionLocator) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendTyping(ctx, locator)
}

func (r *Router) SendProgress(ctx context.Context, locator baldasession.SessionLocator, progress deliverycmd.Progress) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	return adapter.SendProgress(ctx, locator, progress)
}
