package mattermost

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// InboundMessage represents a normalized inbound message from Mattermost.
type InboundMessage struct {
	Locator     deliverycmd.Locator
	MessageID   int
	PostID      string
	RootID      string
	SenderID    string
	SenderName  string
	Text        string
	Direct      bool
	ReceivedAt  time.Time
	Attachments []string
}

// BuildInboundReply converts a threaded Mattermost post into a question
// answer candidate. Mattermost identifies a reply's parent with RootID.
func BuildInboundReply(locator deliverycmd.Locator, subject string, message InboundMessage, receivedAt time.Time) (questioncmd.InboundReply, bool) {
	replyToMessageID := strings.TrimSpace(message.RootID)
	text := strings.TrimSpace(message.Text)
	if replyToMessageID == "" || text == "" {
		return questioncmd.InboundReply{}, false
	}
	return questioncmd.InboundReply{
		Provider:         ChannelType,
		SessionID:        locator.SessionID,
		ConversationKey:  locator.AddressKey,
		ReplyToMessageID: replyToMessageID,
		MessageID:        strings.TrimSpace(message.PostID),
		User:             questioncmd.UserRef{UserID: strings.TrimSpace(subject)},
		Text:             text,
		ReceivedAt:       receivedAt.UTC(),
	}, true
}

// InboundCommand represents a command invocation from Mattermost.
type InboundCommand struct {
	// InvocationID identifies this command invocation for durable deduplication.
	// HTTP slash commands have no Mattermost post, so their ID is derived from
	// the signed request body; websocket commands use the source post ID.
	InvocationID string
	Locator      deliverycmd.Locator
	MessageID    int
	PostID       string
	SenderID     string
	Command      string
	Args         string
	Direct       bool
}

// InboundProcessor processes inbound Mattermost messages and commands.
//
// This mirrors the transport-neutral contract used by the other transports so
// the shared inbound pipeline can be registered without transport branching.
type InboundProcessor interface {
	ProcessInbound(ctx context.Context, msg InboundMessage) (turncmd.InboundSettlement, error)
	HandleCommand(ctx context.Context, cmd InboundCommand) error
	HandleUnsupportedCommand(ctx context.Context, cmd InboundCommand) error
}

const (
	// eventPosted is the Mattermost websocket event for a new post.
	eventPosted = "posted"
	// eventPostEdited is the Mattermost websocket event for an edited post.
	eventPostEdited = "post_edited"
	// eventPostDeleted is the Mattermost websocket event for a deleted post.
	eventPostDeleted = "post_deleted"

	channelTypeDirect    = "D"
	channelTypeGroup     = "G"
	channelTypeOpen      = "O"
	channelTypePrivate   = "P"
	triggerMentionPrefix = "@"
)

// WebSocketEvent is the Mattermost websocket event envelope.
//
// Mattermost encodes `data` as a JSON object in most cases, but the field is
// declared as an arbitrary JSON value by the server. The client decodes it into
// the shapes it needs rather than assuming one form.
type WebSocketEvent struct {
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
	Broadcast struct {
		ChannelID string `json:"channel_id"`
		TeamID    string `json:"team_id"`
		UserID    string `json:"user_id"`
		OmitUsers map[string]bool
	} `json:"broadcast"`
	Seq    int64  `json:"seq"`
	Status string `json:"status"`
}

// PostedData is the payload of a "posted" websocket event. Mattermost sends
// the post as a JSON-encoded string under the "post" key.
type PostedData struct {
	ChannelDisplayName string `json:"channel_display_name"`
	ChannelName        string `json:"channel_name"`
	ChannelType        string `json:"channel_type"`
	Post               string `json:"post"`
	SenderName         string `json:"sender_name"`
	TeamID             string `json:"team_id"`
}

// NormalizeInbound converts a Mattermost post into the transport-neutral
// inbound contract.
func NormalizeInbound(
	locator deliverycmd.Locator,
	post Post,
	senderName string,
	direct bool,
	receivedAt time.Time,
) turncmd.NormalizedInbound {
	providerMessageID := strings.TrimSpace(post.ID)
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("mattermost:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              strings.TrimSpace(post.Message),
		Locator:           locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(post.UserID),
		MessageID:         ParsePostID(providerMessageID),
		ReceivedAt:        receivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMarkdown,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{Typing: false, PlanUpdates: true},
		Direct:            direct,
		Source:            turncmd.SourceMattermost,
	}
}

// LocatorForPost builds the canonical Balda locator for one inbound post.
//
// A reply inside a thread resolves to the thread root so the whole thread maps
// to one session. A channel-level post maps to the channel itself. A direct or
// group message channel maps to the direct-user address.
func LocatorForPost(channel Channel, post Post) deliverycmd.Locator {
	channelID := strings.TrimSpace(post.ChannelID)
	if channelID == "" {
		channelID = strings.TrimSpace(channel.ID)
	}
	if IsGroupChannelType(channel.Type) {
		return NewGroupDMLocator(channelID)
	}
	if IsDirectChannelType(channel.Type) {
		return NewDMLocator(channelID, strings.TrimSpace(post.UserID))
	}
	return NewChannelLocator(channel.TeamID, channelID, strings.TrimSpace(post.RootID))
}

// IsDirectChannelType reports whether a Mattermost channel type is a direct or
// group message conversation rather than a team channel.
func IsDirectChannelType(channelType string) bool {
	switch strings.TrimSpace(channelType) {
	case channelTypeDirect, channelTypeGroup:
		return true
	default:
		return false
	}
}

// IsGroupChannelType reports whether a Mattermost channel type is a group
// direct-message conversation (several participants, one shared channel).
func IsGroupChannelType(channelType string) bool {
	return strings.TrimSpace(channelType) == channelTypeGroup
}

// IsBotEcho reports whether the post was authored by the bot itself.
func IsBotEcho(post Post, botUserID string) bool {
	trimmed := strings.TrimSpace(botUserID)
	if trimmed == "" {
		return false
	}
	return strings.TrimSpace(post.UserID) == trimmed
}

// IsSystemPost reports whether the post is a Mattermost system message (join,
// leave, header change). These carry a non-empty Type and no user-authored text.
func IsSystemPost(post Post) bool {
	return strings.TrimSpace(post.Type) != ""
}

// IsDeletedPost reports whether the post has been deleted.
func IsDeletedPost(post Post) bool {
	return post.DeleteAt > 0
}

// StripMention removes a leading @mention of the bot from inbound text.
//
// In a public channel Balda is addressed by @mention; the mention itself is not
// part of the instruction. Mattermost delivers mentions as plain "@username".
func StripMention(text, botUsername string) string {
	trimmed := strings.TrimSpace(text)
	name := strings.TrimSpace(botUsername)
	if name == "" {
		return trimmed
	}
	mention := triggerMentionPrefix + name
	if !strings.HasPrefix(trimmed, mention) {
		return trimmed
	}
	rest := strings.TrimPrefix(trimmed, mention)
	// Only strip when the mention is a standalone token, not a longer username.
	if rest != "" && !isBoundary(rest[0]) {
		return trimmed
	}
	return strings.TrimSpace(rest)
}

// MentionsBot reports whether the text addresses the bot by name.
func MentionsBot(text, botUsername string) bool {
	name := strings.TrimSpace(botUsername)
	if name == "" {
		return false
	}

	mention := strings.ToLower(triggerMentionPrefix + name)
	lowerText := strings.ToLower(text)
	for start := 0; start < len(lowerText); {
		offset := strings.Index(lowerText[start:], mention)
		if offset < 0 {
			return false
		}
		index := start + offset
		end := index + len(mention)
		if (index == 0 || isBoundary(lowerText[index-1])) &&
			(end == len(lowerText) || isBoundary(lowerText[end])) {
			return true
		}
		start = end
	}
	return false
}

// ParseCommand splits a slash-command post into name and arguments.
//
// It returns ok=false when the text is not a command. The "user invite" spelling
// is normalized to "user add", matching the shared command contract.
func ParseCommand(text string) (name string, args string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", "", false
	}
	name = strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args = ""
	if len(fields) > 1 {
		args = strings.Join(fields[1:], " ")
	}
	if name == "user" && strings.HasPrefix(args, "invite") {
		args = "add" + strings.TrimPrefix(args, "invite")
	}
	if name == "" {
		return "", "", false
	}
	return name, args, true
}

// PostIDFromWebSocket converts the numeric-or-string post identifier used by
// some Mattermost event payloads into a canonical string post ID.
func PostIDFromWebSocket(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	default:
		return ""
	}
}

func isBoundary(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', ',', '.', ':', ';', '!', '?', '(', ')', '[', ']', '{', '}', '\'', '"':
		return true
	default:
		return false
	}
}
