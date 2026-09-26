package mattermost

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

var _ deliverycmd.Adapter = (*Adapter)(nil)

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// Adapter implements deliverycmd.Adapter for the Mattermost transport.
//
// Mattermost has no typing indicator for bots and no draft concept, so those
// operations are deliberate no-ops rather than errors. Progress updates are
// delivered as posts; edit-in-place is used only when the caller supplies a
// provider message ID via ClearQuestionControls settlement.
type Adapter struct {
	client *Client
	logger zerolog.Logger
	// botUserID is needed to address posts and resolve direct channels.
	botUserID string

	now                    func() time.Time
}

// NewAdapter creates a new Mattermost channel adapter.
func NewAdapter(client *Client, logger zerolog.Logger) *Adapter {
	botUserID := ""
	if client != nil {
		botUserID = client.UserID()
	}
	return &Adapter{
		client:           client,
		logger:           logger.With().Str("component", "balda.channel.mattermost").Logger(),
		botUserID:        botUserID,
		now:              time.Now,
	}
}

// SetBotUserID overrides the cached bot identity (useful when it is resolved
// after adapter construction).
func (a *Adapter) SetBotUserID(userID string) {
	if a == nil {
		return
	}
	a.botUserID = strings.TrimSpace(userID)
}

// BotUserID returns the adapter's bot identity.
func (a *Adapter) BotUserID() string {
	if a == nil {
		return ""
	}
	return a.botUserID
}

// Deliver executes one semantic Mattermost delivery operation.
func (a *Adapter) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	var err error
	result := deliverycmd.Result{}
	switch operation.Kind {
	case deliverycmd.OperationPlain:
		err = a.SendPlain(ctx, locator, operation.Text)
	case deliverycmd.OperationMarkdown:
		if operation.Message != nil {
			_, err = a.sendMessage(ctx, locator, *operation.Message)
		} else {
			err = a.SendMarkdownWithFormat(ctx, locator, operation.DeliveryFormat, operation.Text)
		}
	case deliverycmd.OperationAgentReply:
		if operation.Message != nil {
			result.ProviderMessageID, err = a.sendMessage(ctx, locator, *operation.Message)
		} else {
			result.ProviderMessageID, err = a.SendAgentReplyWithProviderMessageIDAndFormat(ctx, locator, operation.DeliveryFormat, operation.Text)
		}
	case deliverycmd.OperationDraft:
		// Mattermost has no draft concept: render the draft as a normal post
		// so the text is never silently dropped.
		err = a.SendPlain(ctx, locator, operation.Text)
	case deliverycmd.OperationTyping:
		err = a.SendTyping(ctx, locator)
	case deliverycmd.OperationProgress:
		err = a.SendProgress(ctx, locator, operation.Progress)
	case deliverycmd.OperationClearQuestionControls:
		err = a.settleQuestionControls(ctx, locator, operation.MessageID, operation.Handle, operation.Text)
	case deliverycmd.OperationPhoto:
		if operation.Media == nil {
			err = fmt.Errorf("mattermost photo operation requires media")
			break
		}
		err = a.sendMedia(ctx, locator, *operation.Media, true)
	case deliverycmd.OperationDocument:
		if operation.Media == nil {
			err = fmt.Errorf("mattermost document operation requires media")
			break
		}
		err = a.sendMedia(ctx, locator, *operation.Media, false)
	default:
		err = fmt.Errorf("unsupported mattermost delivery operation %q", operation.Kind)
	}
	return result, err
}

func (a *Adapter) sendMessage(ctx context.Context, locator deliverycmd.Locator, message deliveryfmt.Message) (string, error) {
	switch message.Name {
	case deliveryfmt.NameMattermostMarkdown:
		return a.sendWithPlainFallback(ctx, locator, message.Text, message.PlainFallback)
	case deliveryfmt.NamePlainText:
		post, err := a.send(ctx, locator, message.Text)
		if err != nil {
			return "", err
		}
		return post.ID, nil
	default:
		return "", fmt.Errorf("unsupported mattermost message format %q", message.Name)
	}
}

// SendPlain sends a plain text message to the locator.
func (a *Adapter) SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	_, err := a.send(ctx, locator, text)
	return err
}

// SendMarkdown sends a Markdown message to the locator.
func (a *Adapter) SendMarkdown(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return a.SendMarkdownWithFormat(ctx, locator, "", text)
}

// SendMarkdownWithFormat sends a Markdown message using Mattermost's delivery
// capability.
func (a *Adapter) SendMarkdownWithFormat(
	ctx context.Context,
	locator deliverycmd.Locator,
	format deliveryfmt.DeliveryFormat,
	text string,
) error {
	if deliveryfmt.NormalizeDeliveryFormat(format) == deliveryfmt.DeliveryFormatNone {
		return a.SendPlain(ctx, locator, MarkdownPlainText(text))
	}
	_, err := a.sendWithPlainFallback(ctx, locator, text, MarkdownPlainText(text))
	return err
}

// SendAgentReply sends agent output to the locator.
func (a *Adapter) SendAgentReply(ctx context.Context, locator deliverycmd.Locator, text string) error {
	_, err := a.sendWithPlainFallback(ctx, locator, text, MarkdownPlainText(text))
	return err
}

// SendAgentReplyWithProviderMessageID sends agent output and returns the
// Mattermost post ID.
func (a *Adapter) SendAgentReplyWithProviderMessageID(
	ctx context.Context,
	locator deliverycmd.Locator,
	text string,
) (string, error) {
	return a.SendAgentReplyWithProviderMessageIDAndFormat(ctx, locator, "", text)
}

// SendAgentReplyWithProviderMessageIDAndFormat sends agent output using
// Mattermost's delivery capability.
func (a *Adapter) SendAgentReplyWithProviderMessageIDAndFormat(
	ctx context.Context,
	locator deliverycmd.Locator,
	format deliveryfmt.DeliveryFormat,
	text string,
) (string, error) {
	if deliveryfmt.NormalizeDeliveryFormat(format) == deliveryfmt.DeliveryFormatNone {
		post, err := a.send(ctx, locator, MarkdownPlainText(text))
		if err != nil {
			return "", err
		}
		return post.ID, nil
	}
	return a.sendWithPlainFallback(ctx, locator, text, MarkdownPlainText(text))
}

// SendTyping is a no-op for Mattermost: its API exposes typing only for
// interactive user sessions, not for bot accounts. Reporting an error here
// would surface as noise for every agent turn.
func (a *Adapter) SendTyping(context.Context, deliverycmd.Locator) error {
	return nil
}

// SendProgress renders semantic progress updates for Mattermost.
func (a *Adapter) SendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	if progress.Policy.Typing {
		if err := a.SendTyping(ctx, locator); err != nil {
			a.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("mattermost typing progress sugar failed")
		}
	}
	if !progress.Visible {
		return nil
	}
	switch progress.Kind {
	case deliverycmd.ProgressThinking:
		return nil
	case deliverycmd.ProgressPlanUpdate:
		return a.SendPlain(ctx, locator, progress.Text)
	default:
		return fmt.Errorf("unsupported mattermost progress kind %q", progress.Kind)
	}
}

// settleQuestionControls applies a resolved interactive control: the original
// post is edited to record the selection so stale buttons are visibly settled.
func (a *Adapter) settleQuestionControls(
	ctx context.Context,
	_ deliverycmd.Locator,
	messageID string,
	handle string,
	selectionText string,
) error {
	postID := strings.TrimSpace(messageID)
	if postID == "" {
		return nil
	}
	text := strings.TrimSpace(selectionText)
	if text == "" {
		return nil
	}
	if handle := strings.TrimSpace(handle); handle != "" {
		text = handle + ": " + text
	}
	if _, err := a.client.UpdatePost(ctx, postID, text); err != nil {
		a.logger.Warn().
			Err(err).
			Str("post_id", postID).
			Msg("mattermost question settlement edit failed")
		return fmt.Errorf("settle mattermost question controls: %w", err)
	}
	return nil
}

// sendMedia uploads a file and posts it into the target conversation.
func (a *Adapter) sendMedia(ctx context.Context, locator deliverycmd.Locator, media deliverycmd.Media, isPhoto bool) error {
	if err := a.validateReady(); err != nil {
		return err
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return fmt.Errorf("decode mattermost locator for media: %w", err)
	}
	if !ok {
		return fmt.Errorf("unsupported channel type %q for mattermost media", locator.ChannelType)
	}
	if strings.TrimSpace(media.LocalPath) == "" {
		// Remote/blob-backed media is not fetched by this transport; the
		// caption still carries the reference so nothing is lost.
		caption := strings.TrimSpace(media.Caption)
		reference := firstNonEmptyString(media.BlobKey, media.FileID)
		if reference != "" {
			caption = strings.TrimSpace(caption + "\n" + reference)
		}
		if caption == "" {
			if isPhoto {
				caption = "(photo unavailable)"
			} else {
				caption = "(file unavailable)"
			}
		}
		return a.SendPlain(ctx, locator, caption)
	}
	file, err := openMediaFile(media.LocalPath)
	if err != nil {
		return fmt.Errorf("open mattermost media %q: %w", media.LocalPath, err)
	}
	defer func() { _ = file.Close() }()

	name := strings.TrimSpace(media.Name)
	if name == "" {
		name = mediaBaseName(media.LocalPath)
	}
	info, err := a.client.UploadFile(ctx, address.ChannelID, name, file)
	if err != nil {
		return fmt.Errorf("upload mattermost media: %w", err)
	}
	_, err = a.client.CreatePostWithFiles(ctx, address.ChannelID, address.RootID, media.Caption, []string{info.ID})
	if err != nil {
		return fmt.Errorf("post mattermost media: %w", err)
	}
	return nil
}

func (a *Adapter) send(
	ctx context.Context,
	locator deliverycmd.Locator,
	text string,
) (Post, error) {
	if err := a.validateReady(); err != nil {
		return Post{}, err
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return Post{}, fmt.Errorf("decode mattermost locator: %w", err)
	}
	if !ok {
		return Post{}, fmt.Errorf("unsupported channel type %q", locator.ChannelType)
	}
	return a.client.CreatePost(ctx, address.ChannelID, address.RootID, text)
}

func (a *Adapter) validateReady() error {
	if a == nil || a.client == nil {
		return fmt.Errorf("mattermost adapter client is required")
	}
	return nil
}

func (a *Adapter) sendWithPlainFallback(
	ctx context.Context,
	locator deliverycmd.Locator,
	text string,
	plainFallback string,
) (string, error) {
	post, err := a.send(ctx, locator, text)
	if err == nil {
		return post.ID, nil
	}
	if !isContentRejectedError(err) {
		return "", err
	}
	fallback := strings.TrimSpace(plainFallback)
	if fallback == "" || fallback == text {
		return "", err
	}
	a.logger.Warn().
		Err(err).
		Str("session_id", locator.SessionID).
		Msg("mattermost rejected markdown content, retrying as plain text")
	fallbackPost, fallbackErr := a.send(ctx, locator, fallback)
	if fallbackErr != nil {
		return "", errors.Join(
			fmt.Errorf("mattermost content rejected before plain text fallback: %w", err),
			fmt.Errorf("send mattermost plain text fallback after content rejection: %w", fallbackErr),
		)
	}
	return fallbackPost.ID, nil
}

// MarkdownPlainText degrades Mattermost Markdown to readable plain text.
func MarkdownPlainText(text string) string {
	plain := markdownImagePattern.ReplaceAllString(text, "$1: $2")
	plain = markdownLinkPattern.ReplaceAllString(plain, "$1 ($2)")
	plain = strings.NewReplacer("**", "", "__", "", "`", "").Replace(plain)
	return strings.TrimSpace(plain)
}

// MessageIDFromInt renders a transport-neutral integer message ID for
// Mattermost posts. Kept for interface parity; MM post IDs are opaque strings.
func MessageIDFromInt(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
