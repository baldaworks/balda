package zulip

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
)

const (
	zulipMessageTypeStream   = "stream"
	zulipTriggerMention      = "mention"
	zulipAddressTypeStream   = "stream"
	zulipAddressTypeDM       = "dm"
	zulipDirectMessageOnlyText = "This command is only available in direct messages."
	zulipNotReadyText         = "Balda is not ready right now."
	zulipResetNotReadyText    = "Balda is not ready right now. Please try again."
	zulipAccessDeniedText     = "Only the bot owner or collaborators can use this bot."
	zulipCancelUsageText      = "Usage: /cancel"
	zulipLocatorUsageText     = "Usage: /locator"
	ownerSessionLabel         = "balda"
	autoSessionLabel          = "auto"
	chatTypePrivate           = "private"
	ownerAlreadyRegisteredMessage = "You are already registered as the bot owner."
	commandStart   = "start"
	commandTopic   = "topic"
	commandLocator = "locator"
	commandCancel  = "cancel"
	commandGoal    = "goalkeeper"
	commandUser    = "user"
	commandUsage   = "usage"
	commandAuto    = "auto"
	commandReset   = "reset"
	commandRestart = "restart"
	commandClose   = "close"
	userActionAdd    = "add"
	userActionInvite = "invite"
	userActionList   = "list"
	userActionRemove = "remove"
	defaultGoalMaxIterations = 25
)

type goalJobService interface {
	HasActiveGoalJob(ctx context.Context, sessionID string) (bool, error)
}

type createSessionCall struct {
	SessionID string
	UserID    string
	AgentName string
}

type zulipInboundMessage struct {
	Locator    deliverycmd.Locator
	MessageID  int
	UserID     string
	Text       string
	Direct     bool
	ReceivedAt time.Time
}

type zulipWebhookPayload struct {
	BotEmail string               `json:"bot_email"`
	Data     string               `json:"data"`
	Trigger  string               `json:"trigger"`
	Token    string               `json:"token"`
	Message  zulipWebhookMessage  `json:"message"`
}

type zulipWebhookMessage struct {
	ID          int    `json:"id"`
	SenderID    int    `json:"sender_id"`
	SenderEmail string `json:"sender_email"`
	Type        string `json:"type"`
	StreamID    int    `json:"stream_id"`
	Subject     string `json:"subject"`
	Content     string `json:"content"`
}

type zulipStartArgs struct {
	Mode  string
	Token string
}

type zulipLocatorAddress struct {
	Type     string `json:"type"`
	StreamID int    `json:"stream_id,omitempty"`
	Topic    string `json:"topic"`
	UserID   int    `json:"user_id,omitempty"`
}

func validateZulipWebhookPayload(payload zulipWebhookPayload) error {
	if payload.Message.SenderID <= 0 {
		return fmt.Errorf("message.sender_id is required")
	}
	if strings.TrimSpace(payload.Message.SenderEmail) == "" {
		return fmt.Errorf("message.sender_email is required")
	}
	switch strings.TrimSpace(payload.Message.Type) {
	case zulipMessageTypeStream:
		if payload.Message.StreamID <= 0 {
			return fmt.Errorf("message.stream_id is required for stream messages")
		}
	case chatTypePrivate:
	default:
		return fmt.Errorf("unsupported message.type %q", payload.Message.Type)
	}
	return nil
}

func verifyZulipWebhookToken(payload zulipWebhookPayload, token string) bool {
	return subtle.ConstantTimeCompare([]byte(payload.Token), []byte(strings.TrimSpace(token))) == 1
}

func isZulipBotEcho(payload zulipWebhookPayload) bool {
	botEmail := strings.TrimSpace(payload.BotEmail)
	if botEmail == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Message.SenderEmail), botEmail)
}

func normalizeZulipMessageText(payload zulipWebhookPayload) string {
	text := firstNonEmptyZulipText(payload.Data, payload.Message.Content)
	if strings.TrimSpace(payload.Trigger) != zulipTriggerMention {
		return text
	}
	return stripLeadingZulipMentions(text)
}

func firstNonEmptyZulipText(values ...string) string {
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text != "" {
			return text
		}
	}
	return ""
}

func stripLeadingZulipMentions(text string) string {
	trimmed := strings.TrimSpace(text)
	for {
		next, ok := trimLeadingZulipMention(trimmed)
		if !ok {
			return trimmed
		}
		trimmed = strings.TrimSpace(next)
	}
}

func trimLeadingZulipMention(text string) (string, bool) {
	for _, prefix := range []string{"@**", "@_**"} {
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		rest := text[len(prefix):]
		end := strings.Index(rest, "**")
		if end < 0 {
			return text, false
		}
		return rest[end+len("**"):], true
	}
	return text, false
}

func zulipUserID(userID int) string {
	return fmt.Sprintf("%s-%d", zulipSessionIDPrefix, userID)
}

func parseZulipUserID(value string) (int, error) {
	prefix := zulipSessionIDPrefix + "-"
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, fmt.Errorf("zulip user id %q must start with %q", value, prefix)
	}
	userID, err := strconv.Atoi(strings.TrimPrefix(trimmed, prefix))
	if err != nil {
		return 0, fmt.Errorf("parse zulip user id %q: %w", value, err)
	}
	if userID <= 0 {
		return 0, fmt.Errorf("zulip user id %q must be positive", value)
	}
	return userID, nil
}

func newZulipStreamLocator(streamID int, topic string) deliverycmd.Locator {
	address := zulipLocatorAddress{Type: zulipAddressTypeStream, StreamID: streamID, Topic: topic}
	raw, _ := json.Marshal(address)
	channelType := string(deliverycmd.ChannelTypeZulip)
	escapedTopic := url.PathEscape(topic)
	addressKey := fmt.Sprintf("s:%d:%s", streamID, escapedTopic)
	addressJSON := string(raw)
	topicHash := sha256.Sum256([]byte(topic))
	hashStr := fmt.Sprintf("%x", topicHash[:4])
	sessionID := fmt.Sprintf("%s-s-%d-%s", zulipSessionIDPrefix, streamID, hashStr)
	locator, err := deliverycmd.NewLocator(channelType, addressKey, addressJSON, sessionID)
	if err != nil {
		return deliverycmd.Locator{ChannelType: channelType, AddressKey: addressKey, AddressJSON: addressJSON, SessionID: sessionID}
	}
	return locator
}

func newZulipDMLocator(userID int) deliverycmd.Locator {
	address := zulipLocatorAddress{Type: zulipAddressTypeDM, UserID: userID}
	raw, _ := json.Marshal(address)
	channelType := string(deliverycmd.ChannelTypeZulip)
	addressKey := fmt.Sprintf("dm:%d", userID)
	addressJSON := string(raw)
	sessionID := fmt.Sprintf("%s-dm-%d", zulipSessionIDPrefix, userID)
	locator, err := deliverycmd.NewLocator(channelType, addressKey, addressJSON, sessionID)
	if err != nil {
		return deliverycmd.Locator{ChannelType: channelType, AddressKey: addressKey, AddressJSON: addressJSON, SessionID: sessionID}
	}
	return locator
}

func locatorFromZulipWebhookPayload(payload zulipWebhookPayload) deliverycmd.Locator {
	if payload.Message.Type == chatTypePrivate {
		return newZulipDMLocator(payload.Message.SenderID)
	}
	return newZulipStreamLocator(payload.Message.StreamID, payload.Message.Subject)
}

func normalizeZulipInbound(message zulipInboundMessage) turncmd.NormalizedInbound {
	providerMessageID := ""
	if message.MessageID > 0 {
		providerMessageID = strconv.Itoa(message.MessageID)
	}
	logicalID := turncmd.InboundID("")
	if providerMessageID != "" {
		logicalID = turncmd.InboundID("zulip:" + providerMessageID)
	}
	return turncmd.NormalizedInbound{
		ID:                logicalID,
		Text:              strings.TrimSpace(message.Text),
		Locator:           message.Locator,
		ProviderMessageID: providerMessageID,
		UserID:            strings.TrimSpace(message.UserID),
		MessageID:         message.MessageID,
		ReceivedAt:        message.ReceivedAt.UTC().Format(time.RFC3339),
		DeliveryFormat:    deliveryfmt.DeliveryFormatMarkdown,
		ProgressPolicy:    deliveryfmt.ProgressPolicy{Typing: true, PlanUpdates: true},
		Direct:            message.Direct,
		Source:            turncmd.SourceZulip,
	}
}

func initZulipOwnerID(store *auth.OwnerStore) (int64, bool) {
	if isNilZulipInterface(store) || !store.HasOwner() {
		return 0, false
	}
	owner := store.GetOwner()
	if owner == nil {
		return 0, false
	}
	for _, subject := range store.OwnerSubjects() {
		value := strings.TrimPrefix(strings.TrimSpace(subject), auth.ChannelZulip+":")
		if value == subject || value == "" {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(value, "%d", &id); err == nil && id > 0 {
			return int64(id), true
		}
	}
	return owner.UserID, true
}

func consumeZulipOwnerBindToken(ctx context.Context, channelAuth *auth.ChannelAuthService, senderID int, token string) (bool, error) {
	if channelAuth == nil {
		return false, fmt.Errorf("token authentication is unavailable")
	}
	return channelAuth.ConsumeOwnerBind(ctx, auth.ChannelZulip, auth.ZulipSubject(senderID), token)
}

func registerZulipOwner(store *auth.OwnerStore, senderID int, expectedToken string, providedToken string) (bool, error) {
	if isNilZulipInterface(store) {
		return false, fmt.Errorf("owner store is unavailable")
	}
	if strings.TrimSpace(providedToken) != strings.TrimSpace(expectedToken) {
		return false, fmt.Errorf("invalid authentication token")
	}
	return store.RegisterOwnerSubject(auth.ZulipSubject(senderID))
}

func normalizeGoalMaxIterations(v int) int {
	if v <= 0 {
		return defaultGoalMaxIterations
	}
	return v
}

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}

func inboundLocator(inbound ingressapp.InboundContext) deliverycmd.Locator {
	return deliverycmd.Locator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}

func firstFieldToken(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 1 {
		return "", false
	}
	token := strings.TrimSpace(fields[0])
	return token, auth.LooksLikeChannelToken(token)
}

func ownerBindTokenBundleMessage(ctx context.Context, authService *auth.ChannelAuthService, createdBy string) (string, bool) {
	if authService == nil {
		return "", false
	}
	tokens, err := authService.CreateMissingOwnerBindTokens(ctx, createdBy)
	if err != nil || len(tokens) == 0 {
		return "", false
	}
	lines := []string{"Connect your other Balda channels:"}
	for _, token := range tokens {
		switch token.Channel {
		case auth.ChannelTelegram:
			lines = append(lines, "", "Telegram:", fmt.Sprintf("DM Balda this command: /start %s", token.Token))
		case auth.ChannelSlack:
			lines = append(lines, "", "Slack:", "DM Balda this token:", token.Token)
		case auth.ChannelZulip:
			lines = append(lines, "", "Zulip:", "DM Balda this token:", token.Token)
		}
	}
	return strings.Join(lines, "\n"), true
}

func canAccessZulipCollaboratorScope(ctx context.Context, ownerStore *auth.OwnerStore, collaboratorStore *auth.CollaboratorStore, userID int64) (bool, error) {
	if !isNilZulipInterface(ownerStore) && ownerStore.IsOwnerSubject(auth.ZulipSubject(int(userID))) {
		return true, nil
	}
	if isNilZulipInterface(collaboratorStore) {
		return false, nil
	}
	_, found, err := collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	return found, err
}

func consumeZulipInvite(ctx context.Context, ownerStore *auth.OwnerStore, inviteStore *auth.InviteStore, collaboratorStore *auth.CollaboratorStore, senderID int, token string) (string, error) {
	userIDStr := fmt.Sprintf("%d", senderID)
	if isNilZulipInterface(ownerStore) {
		return "", fmt.Errorf("owner store is unavailable")
	}
	if ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
		return "You are already the bot owner.", nil
	}
	if !isNilZulipInterface(collaboratorStore) {
		if _, ok, err := collaboratorStore.GetCollaborator(ctx, userIDStr); err != nil {
			return "", err
		} else if ok {
			return "You are already a collaborator.", nil
		}
	}
	if isNilZulipInterface(inviteStore) || isNilZulipInterface(collaboratorStore) {
		return "", fmt.Errorf("invite flow is unavailable")
	}
	invite, err := inviteStore.GetInvite(ctx, token)
	if err != nil {
		return "", err
	}
	if invite == nil {
		return "This invite token is invalid or has expired.", nil
	}
	collaborator := auth.Collaborator{UserID: userIDStr, AddedBy: invite.CreatedBy, AddedAt: time.Now()}
	if err := collaboratorStore.AddCollaborator(ctx, collaborator); err != nil {
		return "", err
	}
	return "Welcome! You are now a bot collaborator.", nil
}

func createZulipInviteToken(ctx context.Context, inviteStore *auth.InviteStore, senderID int) (string, error) {
	if isNilZulipInterface(inviteStore) {
		return "", fmt.Errorf("invite store is unavailable")
	}
	token, _, err := inviteStore.CreateInvite(ctx, fmt.Sprintf("%d", senderID))
	return token, err
}

func loadZulipUserListView(ctx context.Context, collaboratorStore *auth.CollaboratorStore, inviteStore *auth.InviteStore) ([]auth.Collaborator, []auth.Invite, error) {
	if isNilZulipInterface(collaboratorStore) {
		return nil, nil, fmt.Errorf("collaborator store is unavailable")
	}
	collaborators, err := collaboratorStore.ListCollaborators(ctx)
	if err != nil {
		return nil, nil, err
	}
	var invites []auth.Invite
	if !isNilZulipInterface(inviteStore) {
		invites, err = inviteStore.ListInvites(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	return collaborators, invites, nil
}

func removeZulipCollaborator(ctx context.Context, collaboratorStore *auth.CollaboratorStore, userID string) error {
	if isNilZulipInterface(collaboratorStore) {
		return fmt.Errorf("collaborator store is unavailable")
	}
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return fmt.Errorf("user id is required")
	}
	return collaboratorStore.RemoveCollaborator(ctx, trimmed)
}

func isNilZulipInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func parseZulipStartArgs(args string) (zulipStartArgs, bool) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return zulipStartArgs{}, true
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 1 && auth.LooksLikeChannelToken(strings.TrimSpace(fields[0])) {
		return zulipStartArgs{Mode: "channel_token", Token: strings.TrimSpace(fields[0])}, true
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok || strings.TrimSpace(value) == "" {
		return zulipStartArgs{}, false
	}
	mode := strings.TrimSpace(key)
	token := strings.TrimSpace(value)
	switch mode {
	case "owner", "invite":
		return zulipStartArgs{Mode: mode, Token: token}, true
	default:
		return zulipStartArgs{}, false
	}
}

func restartZulipSessionLabel(isDM bool, info baldasession.TopicSessionInfo, ownerLabel, autoLabel string) string {
	if label := strings.TrimSpace(info.AgentName); label != "" {
		return label
	}
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

func restartZulipSessionUserID(senderID int, info baldasession.TopicSessionInfo) string {
	if userID := strings.TrimSpace(info.UserID); userID != "" {
		return userID
	}
	return zulipUserID(senderID)
}

func restartZulipWelcomeDisplayName(isDM bool, label, ownerLabel string) string {
	if !isDM {
		return ownerLabel
	}
	return label
}

func zulipSessionWelcomeLabel(isDM bool, ownerLabel, autoLabel string) string {
	if isDM {
		return ownerLabel
	}
	return autoLabel
}

func ensureZulipSession(ctx context.Context, manager zulipSessionManager, locator baldasession.SessionLocator, transportUserID, providerName string, isDM bool, ownerLabel, autoLabel string) (*baldasession.TopicSession, bool, error) {
	if manager == nil {
		return nil, false, fmt.Errorf("session manager is unavailable")
	}
	existing, _ := manager.GetSession(locator)
	if existing != nil {
		return existing, false, nil
	}
	if strings.TrimSpace(providerName) == "" {
		return nil, false, fmt.Errorf("no provider configured")
	}
	ts, err := manager.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID})
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, false, err
	}
	if err == nil && ts != nil {
		return ts, true, nil
	}
	label := zulipSessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	ts, err = manager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, label)
	if err != nil {
		return nil, false, err
	}
	return ts, true, nil
}

func buildZulipSessionPreparation(ts *baldasession.TopicSession, requesterUserID string) ingressapp.SessionPreparation {
	return ingressapp.SessionPreparation{
		Ready: true, UserID: ts.GetUserID(), RequesterUserID: requesterUserID, AgentSessionID: ts.GetAgentSessionID(),
	}
}

func buildZulipSessionWelcome(manager zulipSessionManager, providerName string, isDM bool, sessionID string, ownerLabel string, autoLabel string) string {
	label := zulipSessionWelcomeLabel(isDM, ownerLabel, autoLabel)
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(label, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipRestartWelcome(manager zulipSessionManager, providerName string, isDM bool, label string, sessionID string, ownerLabel string) string {
	metadata := manager.GetAgentMetadata(providerName)
	welcomeName := restartZulipWelcomeDisplayName(isDM, label, ownerLabel)
	return welcome.BuildAgentWelcomeMessage(welcomeName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipTopicWelcome(manager zulipSessionManager, providerName string, topicName string, sessionID string) string {
	metadata := manager.GetAgentMetadata(providerName)
	return welcome.BuildAgentWelcomeMessage(topicName, sessionID, metadata.Type, metadata.Model, metadata.MCPServers)
}

func buildZulipLocatorMessage(locator deliverycmd.Locator) string {
	ref := locatorref.Format(locator)
	return fmt.Sprintf("Transport: %s\nLocator: %s\n\nUse in scheduler/webhook config:\ntarget: locator\nkey: %s", locator.ChannelType, ref, ref)
}

func zulipAccessDeniedMessage() string { return zulipAccessDeniedText }

func startWelcomeMessage() string {
	return "Welcome to Balda Bot!\n\nTo authenticate:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func startInvalidFormatMessage() string {
	return "Invalid /start format. Use one of:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func startDirectMessageOnly() string           { return zulipDirectMessageOnlyText }
func startOwnerAlreadyRegistered() string      { return "Bot owner is already registered." }
func startOwnerRegistered() string             { return "You are now registered as the bot owner." }
func startOwnerBindUnavailable() string        { return "Token authentication is unavailable right now." }
func startOwnerBindFailed() string             { return "Failed to process token. Please try again." }
func startOwnerBindInvalid() string            { return "This token is invalid or has expired." }
func startOwnerBound() string                  { return "This Zulip account is now connected to the Balda owner." }
func startInviteProcessingFailed() string      { return "Failed to process invite. Ask the operator to check Balda storage configuration." }
func startOwnerStoreUnavailable() string       { return "Could not register owner. Ask the operator to check Balda storage configuration." }
func startInvalidAuthToken() string            { return "Invalid authentication token. Please try again." }
func resetUsageMessage(cmd string) string      { return fmt.Sprintf("Usage: /%s", cmd) }
func resetNotReadyMessage() string             { return zulipResetNotReadyText }
func resetFailedMessage() string               { return "Could not reset this session." }
func restartFailedMessage() string             { return "Could not restart this session." }
func cancelUsageMessage() string               { return zulipCancelUsageText }
func cancelUnavailableMessage() string         { return "Cancel is unavailable right now. Please try again." }
func cancelFailedMessage() string              { return "Could not request cancel." }
func cancelRequestedMessage() string           { return "Cancel requested." }
func locatorUsageMessage() string              { return zulipLocatorUsageText }
func usageUsageMessage() string                { return "Usage: /usage" }
func usageEmptyMessage() string                { return "No provider usage has been recorded for this session yet." }
func closeDirectMessageOnly() string           { return zulipDirectMessageOnlyText }
func closeUsageMessage() string                { return "Usage: /close" }
func closeFailedMessage() string               { return "Could not close this session." }
func closeResetMessage() string                { return "Session history reset." }
func goalUsageMessage() string                 { return "Usage:\n/goalkeeper <objective>\n/goalkeeper clear" }
func goalUnavailableMessage() string           { return "Goal control is unavailable right now. Please try again." }
func goalClearFailedMessage() string           { return "Could not clear goal run." }
func goalStartFailedMessage() string           { return "Could not start goal run." }
func goalAlreadyActiveMessage() string         { return "A goal run is already active for this session." }
func topicDirectMessageOnly() string           { return "This command is only available in stream messages." }
func topicUsageMessage() string                { return "Usage: /topic <name>" }
func topicNotReadyMessage() string             { return zulipNotReadyText }
func topicStreamContextMissingMessage() string { return "Could not determine stream ID from current context." }
func topicCreateFailedMessage() string         { return "Could not create topic session." }
func topicCreatedFallbackMessage(topicName string) string {
	return fmt.Sprintf("Session created for topic '%s'.", topicName)
}
func topicCreatedMessage(topicName string) string {
	return fmt.Sprintf("Session created. Post in topic '%s' to continue.", topicName)
}

func startOwnerAlreadyRegisteredSelfMessage(bundle string) string {
	if strings.TrimSpace(bundle) == "" {
		return "You are already registered as the bot owner."
	}
	return "You are already registered as the bot owner.\n\n" + strings.TrimSpace(bundle)
}

func userUsageMessage() string {
	return "Usage:\n" +
		"• /user add - Generate invite token\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"
}

func userInviteMessage(token string) string {
	return fmt.Sprintf("Invite token created:\n%s\n\nHave the collaborator send:\n/start invite=%s", token, token)
}

func userRemovedMessage(userID string) string {
	return fmt.Sprintf("Collaborator removed: %s", strings.TrimSpace(userID))
}

func userListMessage(collaborators []auth.Collaborator, invites []auth.Invite) string {
	return UserListMessage(collaborators, invites)
}
