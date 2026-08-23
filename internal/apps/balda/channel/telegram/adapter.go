package telegram

import (
	"context"
	"fmt"
	gohtml "html"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	"go.uber.org/fx"
)

var _ deliverycmd.Adapter = (*Adapter)(nil)

const (
	chatTypePrivate     = "private"
	defaultTypingAction = "typing"
	modeRichMarkdown    = "rich_markdown"
	modeRichHTML        = "rich_html"
	modeNone            = "none"
)

type TelegramMessenger interface {
	TelegramFormattingMode() string
	SendPlain(ctx context.Context, chatID int64, text string, topicID int) error
	SendMarkdownWithMode(ctx context.Context, chatID int64, text string, topicID int, mode string) error
	SendAgentReply(ctx context.Context, chatID int64, text string, topicID int) error
	SendAgentReplyMessageLastMessageID(ctx context.Context, chatID int64, message deliveryfmt.Message, topicID int) (int, error)
	SendAgentReplyMessageWithInlineKeyboardLastMessageID(ctx context.Context, chatID int64, message deliveryfmt.Message, topicID int, keyboard client.InlineKeyboardMarkup, fallbackText string) (int, error)
	SendEphemeralAgentReplyMessageWithInlineKeyboardLastMessageID(ctx context.Context, chatID, receiverUserID int64, message deliveryfmt.Message, topicID int, keyboard client.InlineKeyboardMarkup) (int, error)
	SendPlainReply(ctx context.Context, chatID int64, text string, topicID, replyToMessageID int) error
	ClearInlineKeyboard(ctx context.Context, chatID int64, messageID int) error
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
	DeleteEphemeralMessage(ctx context.Context, chatID, receiverUserID int64, ephemeralMessageID int) error
	AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string, showAlert bool) error
	SendDraftPlain(ctx context.Context, chatID int64, draftID int, text string, topicID int) error
	SendDraftMarkdownWithMode(ctx context.Context, chatID int64, draftID int, text string, topicID int, mode string) error
	SendChatAction(ctx context.Context, chatID int64, topicID int, action string) error
	SendPhotoByFileID(ctx context.Context, chatID int64, fileID, caption string, topicID int) error
	SendDocumentByFileID(ctx context.Context, chatID int64, fileID, caption, fileName string, topicID int) error
	SendPhotoByPath(ctx context.Context, chatID int64, localPath, caption string, topicID int) error
	SendDocumentByPath(ctx context.Context, chatID int64, localPath, caption, fileName, mimeType string, topicID int) error
}

// Adapter maps Telegram runtime events and operations to balda session locators.
type Adapter struct {
	messenger          TelegramMessenger
	tgClient           client.ClientWithResponsesInterface
	logger             zerolog.Logger
	planUpdatesEnabled bool

	typingMu               sync.Mutex
	typingThrottleInterval time.Duration
	typingLastSentAt       map[string]time.Time
	now                    func() time.Time

	progressMu          sync.Mutex
	progressDrafts      map[string]int
	nextProgressDraftID int

	mediaGroups *mediaGroupCollector
}

// MessageContext is the balda-facing Telegram message shape.
type MessageContext struct {
	Locator          deliverycmd.Locator
	ChatID           int64
	TopicID          int
	MessageID        int
	ReplyToMessageID int
	UserID           int64
	Entities         []client.MessageEntity
	IsReply          bool
	IsForwarded      bool
	ForwardedFromBot bool
	ReplyToUserID    int64
	ReplyToIsBot     bool
	ReplyContent     string
	ForwardedContent string
	Text             string
	Attachments      []attachment.Descriptor
	HasCommand       bool
	DeliveryOptions  deliveryfmt.Options
	ProgressPolicy   deliveryfmt.ProgressPolicy
	IsDM             bool
	MediaGroupID     string
}

// CommandContext is the balda-facing Telegram command shape.
type CommandContext struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	ChatID          int64
	TopicID         int
	UserID          int64
	Command         string
	Args            string
	IsDM            bool
}

// TopicLifecycleContext is the balda-facing Telegram topic lifecycle shape.
type TopicLifecycleContext struct {
	Locator   deliverycmd.Locator
	ChatID    int64
	TopicID   int
	MessageID int
	UserID    int64
	Type      messagetype.MessageType
}

type AdapterParams struct {
	fx.In

	Messenger          TelegramMessenger
	TGClient           client.ClientWithResponsesInterface
	PlanUpdatesEnabled bool `name:"balda_telegram_plan_updates"`
	Logger             zerolog.Logger
}

// NewAdapter creates the Telegram balda adapter.
func NewAdapter(params AdapterParams) *Adapter {
	return &Adapter{
		messenger:           params.Messenger,
		tgClient:            params.TGClient,
		logger:              params.Logger.With().Str("component", "balda.channel.telegram").Logger(),
		planUpdatesEnabled:  params.PlanUpdatesEnabled,
		typingLastSentAt:    make(map[string]time.Time),
		now:                 time.Now,
		progressDrafts:      make(map[string]int),
		nextProgressDraftID: 1,
		mediaGroups:         newMediaGroupCollector(defaultMediaGroupFlushDelay),
	}
}

// CollectMediaGroup buffers a Telegram media-group item and dispatches one
// combined message context after the group has been quiet for the flush delay.
// Dispatch runs asynchronously with a bounded context. The method returns false
// for ordinary messages, which callers should handle directly.
func (a *Adapter) CollectMediaGroup(message MessageContext, dispatch func(context.Context, MessageContext)) bool {
	if a == nil || a.mediaGroups == nil {
		return false
	}
	return a.mediaGroups.collect(message, dispatch)
}

func (a *Adapter) SetTypingThrottleInterval(interval time.Duration) {
	if a == nil {
		return
	}
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	a.typingThrottleInterval = interval
}

// Deliver executes one semantic Telegram delivery operation.
func (a *Adapter) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	var err error
	result := deliverycmd.Result{}
	switch operation.Kind {
	case deliverycmd.OperationPlain:
		err = a.SendPlain(ctx, locator, operation.Text)
	case deliverycmd.OperationMarkdown:
		if operation.Message != nil {
			err = a.SendMessage(ctx, locator, *operation.Message)
		} else {
			err = a.SendMarkdownWithFormat(ctx, locator, operation.DeliveryFormat, operation.Text)
		}
	case deliverycmd.OperationAgentReply:
		if operation.Message != nil {
			result.ProviderMessageID, err = a.SendAgentReplyMessageWithQuestion(ctx, locator, *operation.Message, operation.Question)
		} else {
			result.ProviderMessageID, err = a.SendAgentReplyWithQuestion(ctx, locator, operation.DeliveryFormat, operation.Text, operation.Question)
		}
	case deliverycmd.OperationDraft:
		err = a.SendDraftPlain(ctx, locator, operation.DraftID, operation.Text)
	case deliverycmd.OperationTyping:
		err = a.SendTyping(ctx, locator)
	case deliverycmd.OperationProgress:
		err = a.SendProgressMessage(ctx, locator, operation.Progress, operation.Message)
	case deliverycmd.OperationClearQuestionControls:
		err = a.SettleQuestionControls(ctx, locator, operation.MessageID, operation.Handle, operation.Text)
	case deliverycmd.OperationPhoto:
		if operation.Media == nil {
			err = fmt.Errorf("telegram photo operation requires media payload")
			break
		}
		if strings.TrimSpace(operation.Media.LocalPath) != "" {
			err = a.SendPhotoByPath(ctx, locator, operation.Media.LocalPath, operation.Media.Caption)
			break
		}
		err = a.SendPhotoByFileID(ctx, locator, operation.Media.FileID, operation.Media.Caption)
	case deliverycmd.OperationDocument:
		if operation.Media == nil {
			err = fmt.Errorf("telegram document operation requires media payload")
			break
		}
		if strings.TrimSpace(operation.Media.LocalPath) != "" {
			err = a.SendDocumentByPath(ctx, locator, operation.Media.LocalPath, operation.Media.Caption, operation.Media.Name, operation.Media.MIMEType)
			break
		}
		err = a.SendDocumentByFileID(ctx, locator, operation.Media.FileID, operation.Media.Caption, operation.Media.Name)
	default:
		err = fmt.Errorf("unsupported telegram delivery operation %q", operation.Kind)
	}
	return result, err
}

func (a *Adapter) shouldSendTyping(locator deliverycmd.Locator) bool {
	if a == nil {
		return false
	}
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	if a.typingThrottleInterval <= 0 {
		return true
	}
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	key := locator.SessionID
	if last, ok := a.typingLastSentAt[key]; ok && now.Sub(last) < a.typingThrottleInterval {
		return false
	}
	a.typingLastSentAt[key] = now
	return true
}

func (a *Adapter) progressDraftID(locator deliverycmd.Locator) int {
	if a == nil {
		return 0
	}
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	key := locator.SessionID
	if draftID := a.progressDrafts[key]; draftID > 0 {
		return draftID
	}
	if a.nextProgressDraftID <= 0 {
		a.nextProgressDraftID = 1
	}
	draftID := a.nextProgressDraftID
	a.nextProgressDraftID++
	a.progressDrafts[key] = draftID
	return draftID
}

// MessageContextFromEvent converts a Telegram message event into balda channel context.
func (a *Adapter) MessageContextFromEvent(event *events.MessageEvent) (MessageContext, bool) {
	if event == nil || event.Message == nil || event.Message.From == nil {
		return MessageContext{}, false
	}

	topicID := a.topicIDFromMessage(event.Message)

	text := ""
	var entities []client.MessageEntity
	if event.Message.Text != nil {
		text = *event.Message.Text
		if event.Message.Entities != nil {
			entities = append(entities, (*event.Message.Entities)...)
		}
	} else if event.Message.Caption != nil {
		text = *event.Message.Caption
		if event.Message.CaptionEntities != nil {
			entities = append(entities, (*event.Message.CaptionEntities)...)
		}
	}
	isReply := event.Message.ReplyToMessage != nil || event.Message.Quote != nil || event.Message.ExternalReply != nil
	isForwarded := event.Message.ForwardOrigin != nil
	replyToUserID := int64(0)
	replyToIsBot := false
	replyToMessageID := 0
	if event.Message.ReplyToMessage != nil && event.Message.ReplyToMessage.From != nil {
		replyToUserID = event.Message.ReplyToMessage.From.Id
		replyToIsBot = event.Message.ReplyToMessage.From.IsBot
		replyToMessageID = event.Message.ReplyToMessage.MessageId
	}
	replyContent := replyContentFromMessage(event.Message)
	forwardedFromBot := forwardedOriginIsBot(event.Message.ForwardOrigin)
	forwardedContent := forwardedContentFromMessage(event.Message)
	attachments := attachmentsFromMessage(event.Message)

	hasCommand := false
	for _, entity := range entities {
		if entity.Type == "bot_command" && entity.Offset == 0 {
			hasCommand = true
			break
		}
	}

	return MessageContext{
		Locator:          NewLocator(event.Message.Chat.Id, topicID),
		ChatID:           event.Message.Chat.Id,
		TopicID:          topicID,
		MessageID:        event.Message.MessageId,
		ReplyToMessageID: replyToMessageID,
		UserID:           event.Message.From.Id,
		Entities:         entities,
		IsReply:          isReply,
		IsForwarded:      isForwarded,
		ForwardedFromBot: forwardedFromBot,
		ReplyToUserID:    replyToUserID,
		ReplyToIsBot:     replyToIsBot,
		ReplyContent:     replyContent,
		ForwardedContent: forwardedContent,
		Text:             text,
		Attachments:      attachments,
		HasCommand:       hasCommand,
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormat(a.telegramFormattingMode()),
			ProgressPolicy: deliveryfmt.ProgressPolicy{
				Typing:      true,
				Thinking:    event.Message.Chat.Type == chatTypePrivate,
				PlanUpdates: a.planUpdatesEnabled,
			},
		},
		ProgressPolicy: deliveryfmt.ProgressPolicy{
			Typing:      true,
			Thinking:    event.Message.Chat.Type == chatTypePrivate,
			PlanUpdates: a.planUpdatesEnabled,
		},
		IsDM:         event.Message.Chat.Type == chatTypePrivate,
		MediaGroupID: mediaGroupID(event.Message),
	}, true
}

func mediaGroupID(message *client.Message) string {
	if message == nil || message.MediaGroupId == nil {
		return ""
	}
	return strings.TrimSpace(*message.MediaGroupId)
}

func attachmentsFromMessage(message *client.Message) []attachment.Descriptor {
	if message == nil {
		return nil
	}
	out := make([]attachment.Descriptor, 0, 3)
	caption := ""
	if message.Caption != nil {
		caption = strings.TrimSpace(*message.Caption)
	}
	if photo := largestPhoto(message.Photo); photo != nil {
		sizeBytes := int64(0)
		if photo.FileSize != nil {
			sizeBytes = int64(*photo.FileSize)
		}
		out = append(out, attachment.Descriptor{
			Kind:         attachment.KindPhoto,
			FileID:       strings.TrimSpace(photo.FileId),
			FileUniqueID: strings.TrimSpace(photo.FileUniqueId),
			SizeBytes:    sizeBytes,
			Caption:      caption,
		})
	}
	if doc := message.Document; doc != nil {
		sizeBytes := int64(0)
		if doc.FileSize != nil {
			sizeBytes = *doc.FileSize
		}
		fileName := ""
		if doc.FileName != nil {
			fileName = strings.TrimSpace(*doc.FileName)
		}
		mimeType := ""
		if doc.MimeType != nil {
			mimeType = strings.TrimSpace(*doc.MimeType)
		}
		out = append(out, attachment.Descriptor{
			Kind:         attachment.KindDocument,
			FileID:       strings.TrimSpace(doc.FileId),
			FileUniqueID: strings.TrimSpace(doc.FileUniqueId),
			FileName:     fileName,
			MIMEType:     mimeType,
			SizeBytes:    sizeBytes,
			Caption:      caption,
		})
	}
	if audio := message.Audio; audio != nil {
		sizeBytes := int64(0)
		if audio.FileSize != nil {
			sizeBytes = *audio.FileSize
		}
		fileName := ""
		if audio.FileName != nil {
			fileName = strings.TrimSpace(*audio.FileName)
		}
		mimeType := ""
		if audio.MimeType != nil {
			mimeType = strings.TrimSpace(*audio.MimeType)
		}
		out = append(out, attachment.Descriptor{
			Kind:         attachment.KindDocument,
			FileID:       strings.TrimSpace(audio.FileId),
			FileUniqueID: strings.TrimSpace(audio.FileUniqueId),
			FileName:     fileName,
			MIMEType:     mimeType,
			SizeBytes:    sizeBytes,
			Caption:      caption,
		})
	}
	if voice := message.Voice; voice != nil {
		sizeBytes := int64(0)
		if voice.FileSize != nil {
			sizeBytes = *voice.FileSize
		}
		mimeType := ""
		if voice.MimeType != nil {
			mimeType = strings.TrimSpace(*voice.MimeType)
		}
		out = append(out, attachment.Descriptor{
			Kind:         attachment.KindVoice,
			FileID:       strings.TrimSpace(voice.FileId),
			FileUniqueID: strings.TrimSpace(voice.FileUniqueId),
			MIMEType:     mimeType,
			SizeBytes:    sizeBytes,
			Caption:      caption,
		})
	}
	return attachment.NormalizeList(out)
}

func largestPhoto(photos *[]client.PhotoSize) *client.PhotoSize {
	if photos == nil || len(*photos) == 0 {
		return nil
	}
	best := &(*photos)[0]
	bestArea := best.Width * best.Height
	for i := 1; i < len(*photos); i++ {
		candidate := &(*photos)[i]
		area := candidate.Width * candidate.Height
		if area > bestArea {
			best = candidate
			bestArea = area
		}
	}
	return best
}

func replyContentFromMessage(message *client.Message) string {
	if message == nil {
		return ""
	}
	if message.Quote != nil && strings.TrimSpace(message.Quote.Text) != "" {
		return message.Quote.Text
	}
	if message.ReplyToMessage == nil {
		return ""
	}
	if message.ReplyToMessage.Text != nil && strings.TrimSpace(*message.ReplyToMessage.Text) != "" {
		return *message.ReplyToMessage.Text
	}
	if message.ReplyToMessage.Caption != nil && strings.TrimSpace(*message.ReplyToMessage.Caption) != "" {
		return *message.ReplyToMessage.Caption
	}
	return richMessagePlainText(message.ReplyToMessage.RichMessage)
}

func forwardedContentFromMessage(message *client.Message) string {
	if message == nil || message.ForwardOrigin == nil {
		return ""
	}
	if message.Text != nil && strings.TrimSpace(*message.Text) != "" {
		return *message.Text
	}
	if message.Caption != nil && strings.TrimSpace(*message.Caption) != "" {
		return *message.Caption
	}
	return richMessagePlainText(message.RichMessage)
}

func forwardedOriginIsBot(origin *client.MessageOrigin) bool {
	if origin == nil {
		return false
	}
	user, ok := (*origin)["user"].(map[string]interface{})
	if !ok {
		return false
	}
	isBot, _ := user["is_bot"].(bool)
	return isBot
}

func richMessagePlainText(rich *client.RichMessage) string {
	if rich == nil || len(rich.Blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(nonEmptyRichParts(richBlocksPlainText(rich.Blocks)), "\n"))
}

func richBlocksPlainText(blocks []client.RichBlock) []string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, richBlockPlainText(block)...)
	}
	return nonEmptyRichParts(parts)
}

func richBlockPlainText(block client.RichBlock) []string {
	if len(block) == 0 {
		return nil
	}
	blockType, _ := block["type"].(string)
	switch blockType {
	case "paragraph", "heading", "footer", "pre", "pullquote", "thinking":
		return richTextAndCreditPlainText(block["text"], block["credit"])
	case "mathematical_expression":
		return richTextParts(block["expression"])
	case "blockquote":
		parts := richBlockArrayPlainText(block["blocks"])
		parts = append(parts, richTextParts(block["credit"])...)
		return nonEmptyRichParts(parts)
	case "collage", "slideshow":
		parts := richBlockArrayPlainText(block["blocks"])
		parts = append(parts, richCaptionPlainText(block["caption"])...)
		return nonEmptyRichParts(parts)
	case "details":
		parts := richTextParts(block["summary"])
		parts = append(parts, richBlockArrayPlainText(block["blocks"])...)
		return nonEmptyRichParts(parts)
	case "list":
		return richListPlainText(block["items"])
	case "table":
		parts := richTablePlainText(block["cells"])
		parts = append(parts, richTextParts(block["caption"])...)
		return nonEmptyRichParts(parts)
	case "animation", "audio", "map", "photo", "video", "voice_note":
		return richCaptionPlainText(block["caption"])
	default:
		return fallbackRichBlockPlainText(block)
	}
}

func richTextAndCreditPlainText(text, credit interface{}) []string {
	parts := richTextParts(text)
	parts = append(parts, richTextParts(credit)...)
	return nonEmptyRichParts(parts)
}

func richCaptionPlainText(value interface{}) []string {
	caption, ok := value.(map[string]interface{})
	if !ok {
		return richTextParts(value)
	}
	parts := richTextParts(caption["text"])
	parts = append(parts, richTextParts(caption["credit"])...)
	return nonEmptyRichParts(parts)
}

func richBlockArrayPlainText(value interface{}) []string {
	switch blocks := value.(type) {
	case []client.RichBlock:
		return richBlocksPlainText(blocks)
	case []interface{}:
		parts := make([]string, 0, len(blocks))
		for _, item := range blocks {
			parts = append(parts, richBlockValuePlainText(item)...)
		}
		return nonEmptyRichParts(parts)
	default:
		return richBlockValuePlainText(value)
	}
}

func richBlockValuePlainText(value interface{}) []string {
	switch v := value.(type) {
	case map[string]interface{}:
		return richBlockPlainText(v)
	default:
		return richTextParts(v)
	}
}

func richListPlainText(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	parts := make([]string, 0, len(items))
	for _, itemValue := range items {
		item, ok := itemValue.(map[string]interface{})
		if !ok {
			continue
		}
		itemParts := richBlockArrayPlainText(item["blocks"])
		if label, ok := item["label"].(string); ok && strings.TrimSpace(label) != "" && len(itemParts) > 0 {
			itemParts[0] = strings.TrimSpace(label) + " " + itemParts[0]
		}
		parts = append(parts, itemParts...)
	}
	return nonEmptyRichParts(parts)
}

func richTablePlainText(value interface{}) []string {
	rows, ok := value.([]interface{})
	if !ok {
		return nil
	}
	parts := make([]string, 0, len(rows))
	for _, rowValue := range rows {
		cells, ok := rowValue.([]interface{})
		if !ok {
			continue
		}
		rowParts := make([]string, 0, len(cells))
		for _, cellValue := range cells {
			cell, ok := cellValue.(map[string]interface{})
			if !ok {
				continue
			}
			rowParts = append(rowParts, richTextParts(cell["text"])...)
		}
		if row := strings.Join(nonEmptyRichParts(rowParts), " | "); row != "" {
			parts = append(parts, row)
		}
	}
	return nonEmptyRichParts(parts)
}

func richTextParts(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []interface{}:
		var out strings.Builder
		for _, item := range v {
			out.WriteString(strings.Join(richTextParts(item), ""))
		}
		if text := strings.TrimSpace(out.String()); text != "" {
			return []string{text}
		}
		return nil
	case map[string]interface{}:
		if text := strings.Join(richTextParts(v["text"]), ""); text != "" {
			return []string{text}
		}
		if text := strings.Join(richTextParts(v["alternative_text"]), ""); text != "" {
			return []string{text}
		}
		if text := strings.Join(richTextParts(v["expression"]), ""); text != "" {
			return []string{text}
		}
		return nil
	default:
		return nil
	}
}

func fallbackRichBlockPlainText(block client.RichBlock) []string {
	keys := []string{"text", "summary", "blocks", "items", "cells", "caption", "credit", "alternative_text", "expression"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, richBlockValuePlainText(block[key])...)
	}
	return nonEmptyRichParts(parts)
}

func nonEmptyRichParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// CommandContextFromEvent converts a Telegram command event into balda channel context.
func (a *Adapter) CommandContextFromEvent(event *events.CommandEvent) (CommandContext, bool) {
	if event == nil || event.Message == nil || event.Message.From == nil {
		return CommandContext{}, false
	}
	topicID := a.topicIDFromMessage(event.Message)

	return CommandContext{
		Locator: NewLocator(event.Message.Chat.Id, topicID),
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormat(a.telegramFormattingMode()),
			ProgressPolicy: deliveryfmt.ProgressPolicy{
				Typing:      true,
				Thinking:    event.Message.Chat.Type == chatTypePrivate,
				PlanUpdates: a.planUpdatesEnabled,
			},
		},
		ChatID:  event.Message.Chat.Id,
		TopicID: topicID,
		UserID:  event.Message.From.Id,
		Command: event.Command,
		Args:    event.Args,
		IsDM:    event.Message.Chat.Type == chatTypePrivate,
	}, true
}

// TopicLifecycleFromEvent converts a Telegram topic lifecycle event into balda channel context.
func (a *Adapter) TopicLifecycleFromEvent(event *events.MessageEvent) (TopicLifecycleContext, bool) {
	if event == nil || event.Message == nil || event.Message.MessageThreadId == nil {
		return TopicLifecycleContext{}, false
	}
	topicID := telegramTopicID(event.Message.Chat.Type, event.Message.MessageThreadId, event.Message.IsTopicMessage)
	if topicID == 0 {
		a.logger.Debug().
			Str("chat_type", event.Message.Chat.Type).
			Int("message_thread_id", *event.Message.MessageThreadId).
			Msg("ignoring topic lifecycle event for non-topic message")
		return TopicLifecycleContext{}, false
	}

	userID := int64(0)
	if event.Message.From != nil {
		userID = event.Message.From.Id
	}

	return TopicLifecycleContext{
		Locator:   NewLocator(event.Message.Chat.Id, topicID),
		ChatID:    event.Message.Chat.Id,
		TopicID:   topicID,
		MessageID: event.Message.MessageId,
		UserID:    userID,
		Type:      event.Type,
	}, true
}

func telegramReasoningMarkdown(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return gohtml.EscapeString(text)
}

func telegramRichMarkdownEnabled(mode string) bool {
	return normalizeTelegramMode(mode) == modeRichMarkdown
}

func telegramPlanUpdateMarkdown(progress deliverycmd.Progress) string {
	if progress.Plan == nil || len(progress.Plan.Entries) == 0 {
		return strings.TrimSpace(progress.Text)
	}
	lines := make([]string, 0, len(progress.Plan.Entries)+2)
	lines = append(lines, "# Plan update", "")
	for _, entry := range progress.Plan.Entries {
		lines = append(lines, telegramPlanChecklistItem(entry.Content, entry.Status))
	}
	return strings.Join(lines, "\n")
}

func telegramPlanChecklistItem(content string, status string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "(no description)"
	}
	switch strings.TrimSpace(status) {
	case "completed":
		return "- [x] " + content
	case "in progress":
		return "- [ ] _In progress:_ " + content
	case "pending":
		return "- [ ] " + content
	case "blocked":
		return "- [ ] _Blocked:_ " + content
	case "failed":
		return "- [ ] _Failed:_ " + content
	case "cancelled":
		return "- [ ] ~~" + content + "~~"
	case "unknown", "":
		return "- [ ] " + content
	default:
		return "- [ ] _" + status + ":_ " + content
	}
}

// SendPlain sends a plain text reply to the locator.
func (a *Adapter) SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	return a.messenger.SendPlain(ctx, chatID, text, topicID)
}

// SendMarkdown sends a Markdown reply to the locator.
func (a *Adapter) SendMarkdown(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return a.SendMarkdownWithFormat(ctx, locator, "", text)
}

// SendMarkdownWithFormat sends a Markdown reply using a request-scoped delivery capability.
func (a *Adapter) SendMarkdownWithFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	mode := effectiveTelegramMode(format, a.telegramFormattingMode())
	return a.messenger.SendMarkdownWithMode(ctx, chatID, text, topicID, mode)
}

// SendMessage delivers one registry-formatted Telegram message.
func (a *Adapter) SendMessage(ctx context.Context, locator deliverycmd.Locator, message deliveryfmt.Message) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	_, err = a.messenger.SendAgentReplyMessageLastMessageID(ctx, chatID, message, topicID)
	return err
}

// SendAgentReply sends final agent output for the locator using configured formatting mode.
func (a *Adapter) SendAgentReply(ctx context.Context, locator deliverycmd.Locator, text string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	return a.messenger.SendAgentReply(ctx, chatID, text, topicID)
}

// SendAgentReplyWithProviderMessageID sends final agent output and returns the provider message ID when available.
func (a *Adapter) SendAgentReplyWithProviderMessageID(ctx context.Context, locator deliverycmd.Locator, text string) (string, error) {
	return a.SendAgentReplyWithProviderMessageIDAndFormat(ctx, locator, "", text)
}

// SendAgentReplyWithProviderMessageIDAndFormat sends final agent output using a request-scoped delivery capability.
func (a *Adapter) SendAgentReplyWithProviderMessageIDAndFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) (string, error) {
	return a.SendAgentReplyWithQuestion(ctx, locator, format, text, nil)
}

// SendAgentReplyWithQuestion projects generic question options into Telegram
// inline controls while preserving a text-only fallback.
func (a *Adapter) SendAgentReplyWithQuestion(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string, question *deliverycmd.Question) (string, error) {
	mode := effectiveTelegramMode(format, a.telegramFormattingMode())
	message := deliveryfmt.Message{Name: deliveryfmt.NameTelegramRichMarkdown, Text: text, PlainFallback: text}
	switch mode {
	case modeRichHTML:
		message.Name = deliveryfmt.NameTelegramRichHTML
		message.Text = telegramfmt.HTML(text)
		message.PlainFallback = telegramfmt.HTMLPlainText(text)
	case modeNone:
		message.Name = deliveryfmt.NamePlainText
	case modeRichMarkdown:
	default:
		return "", fmt.Errorf("unsupported telegram formatting mode %q", mode)
	}
	return a.SendAgentReplyMessageWithQuestion(ctx, locator, message, question)
}

// SendAgentReplyMessageWithQuestion projects generic question options into
// Telegram controls while preserving the registry-derived safe plain fallback.
func (a *Adapter) SendAgentReplyMessageWithQuestion(ctx context.Context, locator deliverycmd.Locator, message deliveryfmt.Message, question *deliverycmd.Question) (string, error) {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return "", err
	}
	var lastMessageID int
	switch {
	case question == nil:
		lastMessageID, err = a.messenger.SendAgentReplyMessageLastMessageID(ctx, chatID, message, topicID)
	case question.Audience.Visibility == deliverycmd.QuestionVisibilityPrivate:
		receiverUserID, parseErr := telegramref.ParseUserID(question.Audience.UserID)
		if parseErr != nil {
			return "", fmt.Errorf("resolve private telegram question audience: %w", parseErr)
		}
		keyboard, keyboardErr := questionInlineKeyboard(*question)
		if keyboardErr != nil {
			return "", fmt.Errorf("build private telegram question controls: %w", keyboardErr)
		}
		if chatID == receiverUserID {
			lastMessageID, err = a.messenger.SendAgentReplyMessageWithInlineKeyboardLastMessageID(
				ctx, chatID, message, topicID, keyboard, questionTextFallback(message.PlainFallback, *question),
			)
		} else {
			var ephemeralMessageID int
			ephemeralMessageID, err = a.messenger.SendEphemeralAgentReplyMessageWithInlineKeyboardLastMessageID(
				ctx, chatID, receiverUserID, message, topicID, keyboard,
			)
			if err == nil {
				return ephemeralProviderMessageID(receiverUserID, ephemeralMessageID), nil
			}
		}
	default:
		keyboard, keyboardErr := questionInlineKeyboard(*question)
		if keyboardErr != nil {
			a.logger.Warn().Err(keyboardErr).Str("question_id", question.ID).Msg("build telegram question controls failed, using text choices")
			plain := deliveryfmt.Message{
				Name:          deliveryfmt.NamePlainText,
				Text:          questionTextFallback(message.PlainFallback, *question),
				PlainFallback: questionTextFallback(message.PlainFallback, *question),
			}
			lastMessageID, err = a.messenger.SendAgentReplyMessageLastMessageID(ctx, chatID, plain, topicID)
		} else {
			lastMessageID, err = a.messenger.SendAgentReplyMessageWithInlineKeyboardLastMessageID(
				ctx,
				chatID,
				message,
				topicID,
				keyboard,
				questionTextFallback(message.PlainFallback, *question),
			)
		}
	}
	if err != nil {
		return "", err
	}
	if lastMessageID <= 0 {
		return "", nil
	}
	return strconv.Itoa(lastMessageID), nil
}

// ClearQuestionControls removes the inline keyboard from a previously sent
// Telegram question.
func (a *Adapter) ClearQuestionControls(ctx context.Context, locator deliverycmd.Locator, messageID, handle string) error {
	return a.SettleQuestionControls(ctx, locator, messageID, handle, "")
}

// SettleQuestionControls removes interactive controls while preserving normal
// messages and records a structured choice as a native reply. Ephemeral
// prompts remain private and are deleted instead of producing a public reply.
func (a *Adapter) SettleQuestionControls(ctx context.Context, locator deliverycmd.Locator, messageID, handle, selectionText string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(handle) {
	case telegramQuestionControlHandleDeleteMessage:
		id, err := strconv.Atoi(strings.TrimSpace(messageID))
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid telegram message id %q", messageID)
		}
		return a.messenger.DeleteMessage(ctx, chatID, id)
	case "", telegramQuestionControlHandleClearInlineKeyboard:
	default:
		return fmt.Errorf("unsupported telegram question control handle %q", handle)
	}
	if receiverUserID, ephemeralMessageID, ok := parseEphemeralProviderMessageID(messageID); ok {
		return a.messenger.DeleteEphemeralMessage(ctx, chatID, receiverUserID, ephemeralMessageID)
	}
	id, err := strconv.Atoi(strings.TrimSpace(messageID))
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid telegram message id %q", messageID)
	}
	if err := a.messenger.ClearInlineKeyboard(ctx, chatID, id); err != nil {
		return err
	}
	selectionText = strings.TrimSpace(selectionText)
	if selectionText == "" {
		return nil
	}
	return a.messenger.SendPlainReply(ctx, chatID, "Your selection: "+selectionText, topicID, id)
}

// SendDraftPlain updates a draft message for the locator.
func (a *Adapter) SendDraftPlain(ctx context.Context, locator deliverycmd.Locator, draftID int, text string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	return a.messenger.SendDraftPlain(ctx, chatID, draftID, text, topicID)
}

// SendTyping sends a typing chat action to the locator chat/topic.
func (a *Adapter) SendTyping(ctx context.Context, locator deliverycmd.Locator) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	if !a.shouldSendTyping(locator) {
		return nil
	}
	return a.messenger.SendChatAction(ctx, chatID, topicID, defaultTypingAction)
}

// SendProgress renders a semantic conversational progress update for Telegram.
func (a *Adapter) SendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	return a.SendProgressMessage(ctx, locator, progress, nil)
}

// SendProgressMessage renders progress with the resolved message format when
// the durable delivery workflow supplied one.
func (a *Adapter) SendProgressMessage(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress, message *deliveryfmt.Message) error {
	richMarkdown := telegramRichMarkdownEnabled(a.telegramFormattingMode())
	if message != nil {
		richMarkdown = message.Name == deliveryfmt.NameTelegramRichMarkdown
	}
	if progress.Policy.Typing {
		if err := a.SendTyping(ctx, locator); err != nil {
			a.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("telegram typing progress sugar failed")
		}
	}
	if !progress.Visible {
		return nil
	}
	switch progress.Kind {
	case deliverycmd.ProgressThinking:
		a.logger.Debug().
			Str("session_id", locator.SessionID).
			Bool("visible", progress.Visible).
			Bool("policy_thinking", progress.Policy.Thinking).
			Int("text_char_count", len(strings.TrimSpace(progress.Text))).
			Int("sequence", progress.Sequence).
			Msg("telegram thinking progress received")
		if !progress.Policy.Thinking {
			return nil
		}
		if strings.TrimSpace(progress.Text) == "" {
			return nil
		}
		chatID, topicID, err := telegramTuple(locator)
		if err != nil {
			return err
		}
		draftID := a.progressDraftID(locator)
		a.logger.Debug().
			Str("session_id", locator.SessionID).
			Int("draft_id", draftID).
			Bool("rich_markdown", richMarkdown).
			Msg("telegram rendering thinking progress")
		if richMarkdown {
			return a.messenger.SendDraftMarkdownWithMode(ctx, chatID, draftID, telegramReasoningMarkdown(progress.Text), topicID, modeRichMarkdown)
		}
		return a.messenger.SendDraftPlain(ctx, chatID, draftID, progress.Text, topicID)
	case deliverycmd.ProgressPlanUpdate:
		chatID, topicID, err := telegramTuple(locator)
		if err != nil {
			return err
		}
		if richMarkdown {
			markdown := telegramPlanUpdateMarkdown(progress)
			if progress.Policy.Thinking {
				return a.messenger.SendDraftMarkdownWithMode(ctx, chatID, a.progressDraftID(locator), markdown, topicID, modeRichMarkdown)
			}
			return a.messenger.SendMarkdownWithMode(ctx, chatID, markdown, topicID, modeRichMarkdown)
		}
		if progress.Policy.Thinking {
			return a.messenger.SendDraftPlain(ctx, chatID, a.progressDraftID(locator), progress.Text, topicID)
		}
		return a.messenger.SendPlain(ctx, chatID, progress.Text, topicID)
	default:
		return fmt.Errorf("unsupported telegram progress kind %q", progress.Kind)
	}
}

func (a *Adapter) SendPhotoByFileID(ctx context.Context, locator deliverycmd.Locator, fileID, caption string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	return a.messenger.SendPhotoByFileID(ctx, chatID, fileID, caption, topicID)
}

func (a *Adapter) SendDocumentByFileID(ctx context.Context, locator deliverycmd.Locator, fileID, caption, fileName string) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	return a.messenger.SendDocumentByFileID(ctx, chatID, fileID, caption, fileName, topicID)
}

func (a *Adapter) SendPhotoByPath(ctx context.Context, locator deliverycmd.Locator, localPath, caption string) error {
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("telegram locator is required")
	}
	return a.messenger.SendPhotoByPath(ctx, address.ChatID, localPath, caption, address.TopicID)
}

func (a *Adapter) SendDocumentByPath(ctx context.Context, locator deliverycmd.Locator, localPath, caption, fileName, mimeType string) error {
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("telegram locator is required")
	}
	return a.messenger.SendDocumentByPath(ctx, address.ChatID, localPath, caption, fileName, mimeType, address.TopicID)
}

// CreateTopicLocator creates a Telegram forum topic and returns the balda locator for it.
func (a *Adapter) CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error) {
	createTopicResp, err := a.tgClient.CreateForumTopicWithResponse(ctx, client.CreateForumTopicJSONRequestBody{
		ChatId: chatID,
		Name:   topicName,
	})
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("creating forum topic: %w", err)
	}
	if createTopicResp.JSON200 == nil {
		return deliverycmd.Locator{}, fmt.Errorf("failed to create forum topic: %s", createTopicResp.Status())
	}

	return NewLocator(chatID, createTopicResp.JSON200.Result.MessageThreadId), nil
}

// Close removes a Telegram forum topic for the locator. Root locators are ignored.
func (a *Adapter) Close(ctx context.Context, locator deliverycmd.Locator) error {
	chatID, topicID, err := telegramTuple(locator)
	if err != nil {
		return err
	}
	if topicID == 0 {
		return nil
	}

	closeResp, err := a.tgClient.DeleteForumTopicWithResponse(ctx, client.DeleteForumTopicJSONRequestBody{
		ChatId:          chatID,
		MessageThreadId: topicID,
	})
	if err != nil {
		return fmt.Errorf("removing forum topic: %w", err)
	}
	if closeResp.JSON200 == nil {
		return fmt.Errorf("failed to remove forum topic: %s", closeResp.Status())
	}
	return nil
}

func telegramTuple(locator deliverycmd.Locator) (int64, int, error) {
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return 0, 0, fmt.Errorf("decode telegram locator %q: %w", locator.SessionID, err)
	}
	if !ok {
		return 0, 0, fmt.Errorf("unsupported channel type %q", locator.ChannelType)
	}
	return address.ChatID, address.TopicID, nil
}

func effectiveTelegramMode(format deliveryfmt.DeliveryFormat, fallback string) string {
	normalized := deliveryfmt.NormalizeDeliveryFormat(format)
	if normalized == "" {
		return normalizeTelegramMode(fallback)
	}
	return normalizeTelegramMode(string(normalized))
}

func normalizeTelegramMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "":
		return modeRichMarkdown
	case modeRichMarkdown:
		return modeRichMarkdown
	case modeRichHTML:
		return modeRichHTML
	case modeNone:
		return modeNone
	default:
		return normalized
	}
}

func (a *Adapter) telegramFormattingMode() string {
	if a == nil || a.messenger == nil {
		return modeRichMarkdown
	}
	return a.messenger.TelegramFormattingMode()
}

func (a *Adapter) topicIDFromMessage(msg *client.Message) int {
	if msg == nil || msg.MessageThreadId == nil {
		return 0
	}
	topicID := telegramTopicID(msg.Chat.Type, msg.MessageThreadId, msg.IsTopicMessage)
	if topicID == 0 && msg.Chat.Type == chatTypePrivate {
		a.logger.Debug().
			Str("chat_type", msg.Chat.Type).
			Int("message_thread_id", *msg.MessageThreadId).
			Msg("ignoring message_thread_id for non-topic message")
	}
	return topicID
}

func telegramTopicID(chatType string, messageThreadID *int, topicMessage *bool) int {
	if messageThreadID == nil {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(chatType), chatTypePrivate) {
		// In public chats, only explicit forum-topic messages should route to a
		// topic-scoped Balda session. Ordinary replies may still carry
		// message_thread_id metadata, but they must stay on the root session.
		if topicMessage != nil && *topicMessage {
			return *messageThreadID
		}
		return 0
	}
	if topicMessage != nil && !*topicMessage {
		return 0
	}
	// For private chats, an explicit false marks a regular DM. If Telegram
	// omits is_topic_message, the thread identifier remains authoritative.
	return *messageThreadID
}
