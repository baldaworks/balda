package channel

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

// ChannelAdapter is the transport-neutral interface for sending messages
// and semantic progress updates to a balda session.
type ChannelAdapter interface {
	SendPlain(ctx context.Context, locator baldasession.SessionLocator, text string) error
	SendMarkdown(ctx context.Context, locator baldasession.SessionLocator, text string) error
	SendMarkdownWithProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) error
	SendAgentReply(ctx context.Context, locator baldasession.SessionLocator, text string) error
	SendAgentReplyWithProviderMessageID(ctx context.Context, locator baldasession.SessionLocator, text string) (string, error)
	SendAgentReplyWithProviderMessageIDAndProfile(ctx context.Context, locator baldasession.SessionLocator, profile deliverycmd.Profile, text string) (string, error)
	SendDraftPlain(ctx context.Context, locator baldasession.SessionLocator, draftID int, text string) error
	SendTyping(ctx context.Context, locator baldasession.SessionLocator) error
	SendProgress(ctx context.Context, locator baldasession.SessionLocator, progress deliverycmd.Progress) error
}
