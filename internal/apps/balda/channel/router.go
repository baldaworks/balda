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
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationPlain, Text: text})
	return err
}

func (r *Router) SendMarkdown(ctx context.Context, locator baldasession.SessionLocator, text string) error {
	return r.SendMarkdownWithProfile(ctx, locator, deliverycmd.Profile{}, text)
}

func (r *Router) SendMarkdownWithProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationMarkdown, Profile: profile, Text: text})
	return err
}

func (r *Router) SendAgentReply(ctx context.Context, locator baldasession.SessionLocator, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationAgentReply, Text: text})
	return err
}

func (r *Router) SendAgentReplyWithProviderMessageID(ctx context.Context, locator baldasession.SessionLocator, text string) (string, error) {
	return r.SendAgentReplyWithProviderMessageIDAndProfile(ctx, locator, deliverycmd.Profile{}, text)
}

func (r *Router) SendAgentReplyWithProviderMessageIDAndProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) (string, error) {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return "", err
	}
	result, err := adapter.Deliver(ctx, locator, Operation{Kind: OperationAgentReply, Profile: profile, Text: text})
	return result.ProviderMessageID, err
}

func (r *Router) SendDraftPlain(ctx context.Context, locator baldasession.SessionLocator, draftID int, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationDraft, DraftID: draftID, Text: text})
	return err
}

func (r *Router) SendTyping(ctx context.Context, locator baldasession.SessionLocator) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationTyping})
	return err
}

func (r *Router) SendProgress(ctx context.Context, locator baldasession.SessionLocator, progress deliverycmd.Progress) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, Operation{Kind: OperationProgress, Progress: progress})
	return err
}
