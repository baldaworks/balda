package handlersfx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

type fakeMattermostOwnerKVStore struct{}

func (fakeMattermostOwnerKVStore) GetJSON(context.Context, string) (any, bool, error) {
	return nil, false, nil
}
func (fakeMattermostOwnerKVStore) SetJSON(context.Context, string, any) error { return nil }
func (fakeMattermostOwnerKVStore) SetWithTTL(context.Context, string, any, time.Duration) error {
	return nil
}
func (fakeMattermostOwnerKVStore) Delete(context.Context, string) error           { return nil }
func (fakeMattermostOwnerKVStore) List(context.Context, string) ([]string, error) { return nil, nil }

type recordingMattermostCommandIngress struct{ requests []commandcmd.Request }

func (r *recordingMattermostCommandIngress) PublishCommand(_ context.Context, req commandcmd.Request) error {
	r.requests = append(r.requests, req)
	return nil
}

type recordingMattermostChatHandler struct {
	requests []chatapp.Request
	result   chatapp.Result
	err      error
}

func (h *recordingMattermostChatHandler) HandleChat(_ context.Context, request chatapp.Request) (chatapp.Result, error) {
	h.requests = append(h.requests, request)
	return h.result, h.err
}

func newOwnerStoreWithMattermostSubject(t *testing.T, userID string) *auth.OwnerStore {
	t.Helper()
	ownerStore, err := auth.NewOwnerStore(&fakeMattermostOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := ownerStore.RegisterOwnerSubject(auth.MattermostSubject(userID)); err != nil {
		t.Fatalf("RegisterOwnerSubject() error = %v", err)
	}
	return ownerStore
}

func TestMattermostHandlerPublishesEverySupportedCommand(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)

	for _, name := range mattermost.SupportedCommands() {
		t.Run(name, func(t *testing.T) {
			ingress := &recordingMattermostCommandIngress{}
			h := &mattermostInboundHandler{
				ownerStore:     ownerStore,
				commandIngress: ingress,
				logger:         zerolog.Nop(),
			}

			err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
				Locator:  mattermost.NewDMLocator("dm-1", ownerID),
				PostID:   "post-1",
				SenderID: ownerID,
				Command:  name,
				Direct:   true,
			})
			if err != nil {
				t.Fatalf("HandleCommand(%s) error = %v", name, err)
			}
			if len(ingress.requests) != 1 {
				t.Fatalf("published requests = %d, want 1", len(ingress.requests))
			}
			got := ingress.requests[0]
			if got.InvocationID != "mattermost:command:post-1" {
				t.Fatalf("InvocationID = %q, want %q", got.InvocationID, "mattermost:command:post-1")
			}
			if got.Payload.Name != name {
				t.Fatalf("payload.Name = %q, want %q", got.Payload.Name, name)
			}
			if got.Payload.Transport != mattermost.ChannelType {
				t.Fatalf("payload.Transport = %q, want %q", got.Payload.Transport, mattermost.ChannelType)
			}
			if !got.Payload.Access.Owner {
				t.Fatal("payload.Access.Owner = false, want true for the registered owner")
			}
		})
	}
}

func TestMattermostHandlerUsesExplicitInvocationID(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	ingress := &recordingMattermostCommandIngress{}
	h := &mattermostInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop()}

	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		InvocationID: "mattermost:command:body-hash",
		Locator:      mattermost.NewDMLocator("dm-1", ownerID),
		SenderID:     ownerID,
		Command:      "locator",
		Direct:       true,
	}); err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}
	if len(ingress.requests) != 1 {
		t.Fatalf("published requests = %d, want 1", len(ingress.requests))
	}
	if got, want := ingress.requests[0].InvocationID, "mattermost:command:body-hash"; got != want {
		t.Fatalf("InvocationID = %q, want %q", got, want)
	}
}

func TestMattermostHandlerSendsChannelQualifiedPrincipalExactlyOnce(t *testing.T) {
	// Regression guard: the transport prefix must appear exactly once. A doubled
	// prefix ("mattermost:mattermost:<id>") never matches a stored binding, so
	// every authorized action would silently fail.
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	ingress := &recordingMattermostCommandIngress{}
	h := &mattermostInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop()}

	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", ownerID),
		PostID:   "post-1",
		SenderID: ownerID,
		Command:  commandStart,
		Direct:   true,
	}); err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}

	if len(ingress.requests) != 1 {
		t.Fatalf("published requests = %d, want 1", len(ingress.requests))
	}
	principal := ingress.requests[0].Payload.Principal
	if got, want := principal, "mattermost:owner-user-1"; got != want {
		t.Fatalf("payload.Principal = %q, want %q", got, want)
	}
	if principal != auth.MattermostSubject(ownerID) {
		t.Fatalf("payload.Principal = %q, want the canonical subject %q", principal, auth.MattermostSubject(ownerID))
	}
}

func TestMattermostSubjectIsIdempotent(t *testing.T) {
	// The helper must tolerate a value that is already channel-qualified, because
	// callers sit at different layers and one of them passes a full subject.
	const userID = "user-1"
	want := "mattermost:user-1"

	if got := auth.MattermostSubject(userID); got != want {
		t.Fatalf("MattermostSubject(raw) = %q, want %q", got, want)
	}
	if got := auth.MattermostSubject(auth.MattermostSubject(userID)); got != want {
		t.Fatalf("MattermostSubject(subject) = %q, want %q without a doubled prefix", got, want)
	}
	if got := auth.MattermostSubject("  mattermost:user-1  "); got != want {
		t.Fatalf("MattermostSubject(padded subject) = %q, want %q", got, want)
	}
}

func TestMattermostHandlerRejectsUnauthorizedCommand(t *testing.T) {
	ownerStore := newOwnerStoreWithMattermostSubject(t, "owner-user-1")
	ingress := &recordingMattermostCommandIngress{}
	h := &mattermostInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop()}

	// A stranger must not reach the command pipeline. The denial notice itself
	// cannot be delivered without a live runtime, so an error is expected here —
	// what matters is that nothing was published.
	err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", "stranger-1"),
		PostID:   "post-1",
		SenderID: "stranger-1",
		Command:  "topic",
		Direct:   true,
	})
	if err != nil && !strings.Contains(err.Error(), "runtime is unavailable") {
		t.Fatalf("HandleCommand() error = %v, want nil or a runtime-unavailable error", err)
	}

	if len(ingress.requests) != 0 {
		t.Fatalf("published requests = %d, want 0 for an unauthorized sender", len(ingress.requests))
	}
}

func TestMattermostHandlerAllowsStartBeforeAuthorization(t *testing.T) {
	// /start is the onboarding entrypoint: the owner does not exist yet, so it
	// must be reachable by an unauthorized sender. Otherwise the transport can
	// never be bootstrapped.
	ownerStore, err := auth.NewOwnerStore(&fakeMattermostOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	ingress := &recordingMattermostCommandIngress{}
	h := &mattermostInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop()}

	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", "future-owner"),
		PostID:   "post-1",
		SenderID: "future-owner",
		Command:  commandStart,
		Args:     "owner=token",
		Direct:   true,
	}); err != nil {
		t.Fatalf("HandleCommand(/start) error = %v", err)
	}

	if len(ingress.requests) != 1 {
		t.Fatalf("published requests = %d, want 1 — /start must be reachable while unregistered", len(ingress.requests))
	}
	if got, want := ingress.requests[0].Payload.Name, commandStart; got != want {
		t.Fatalf("payload.Name = %q, want %q", got, want)
	}
}

func TestMattermostHandlerUsesMarkdownWithoutTypingProgress(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	ingress := &recordingMattermostCommandIngress{}
	h := &mattermostInboundHandler{ownerStore: ownerStore, commandIngress: ingress, logger: zerolog.Nop()}

	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", ownerID),
		PostID:   "post-1",
		SenderID: ownerID,
		Command:  "topic",
		Direct:   true,
	}); err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}

	presentation := ingress.requests[0].Payload.Presentation
	if got, want := presentation.DeliveryFormat, deliveryfmt.DeliveryFormatMarkdown; got != want {
		t.Fatalf("DeliveryFormat = %q, want %q", got, want)
	}
	if presentation.ProgressPolicy.Typing {
		t.Fatal("ProgressPolicy.Typing = true, want false: Mattermost bots have no typing API")
	}
	if !presentation.ProgressPolicy.PlanUpdates {
		t.Fatal("ProgressPolicy.PlanUpdates = false, want true")
	}
}

func TestMattermostHandlerWithoutIngressIsSafe(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", ownerID),
		PostID:   "post-1",
		SenderID: ownerID,
		Command:  "topic",
		Direct:   true,
	}); err != nil {
		t.Fatalf("HandleCommand() error = %v, want nil when no command pipeline is wired", err)
	}
}

func TestMattermostHandlerAuthorizeMattermostUserAllowsOwnerOnly(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	allowed, err := h.authorizeMattermostUser(context.Background(), ownerID)
	if err != nil {
		t.Fatalf("authorizeMattermostUser(owner) error = %v", err)
	}
	if !allowed {
		t.Fatal("authorizeMattermostUser(owner) = false, want true")
	}

	denied, err := h.authorizeMattermostUser(context.Background(), "stranger-1")
	if err != nil {
		t.Fatalf("authorizeMattermostUser(stranger) error = %v", err)
	}
	if denied {
		t.Fatal("authorizeMattermostUser(stranger) = true, want false")
	}
}

func TestMattermostHandlerAuthorizeMattermostUserRejectsEmptyUser(t *testing.T) {
	ownerStore := newOwnerStoreWithMattermostSubject(t, "owner-user-1")
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	result, err := h.authorizeMattermostUser(context.Background(), "")
	if err != nil {
		t.Fatalf("authorizeMattermostUser(empty) error = %v", err)
	}
	if result {
		t.Fatal("authorizeMattermostUser(empty) = true, want false")
	}
}

func TestMattermostHandlerCanAccessAndIsOwner(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	if !h.canAccess(context.Background(), ownerID) {
		t.Fatal("canAccess(owner) = false, want true")
	}
	if h.canAccess(context.Background(), "stranger-1") {
		t.Fatal("canAccess(stranger) = true, want false")
	}
	if !h.isOwner(ownerID) {
		t.Fatal("isOwner(owner) = false, want true")
	}
	if h.isOwner("stranger-1") {
		t.Fatal("isOwner(stranger) = true, want false")
	}
}

func TestMattermostHandlerWithoutOwnerStoreDeniesAccess(t *testing.T) {
	h := &mattermostInboundHandler{logger: zerolog.Nop()}

	if h.canAccess(context.Background(), "user-1") {
		t.Fatal("canAccess() = true without an owner store, want false")
	}
	if h.isOwner("user-1") {
		t.Fatal("isOwner() = true without an owner store, want false")
	}
	if result, err := h.authorizeMattermostUser(context.Background(), "user-1"); err != nil {
		t.Fatalf("authorizeMattermostUser() error = %v", err)
	} else if result {
		t.Fatal("authorizeMattermostUser() = true without an owner store, want false")
	}
}

func TestMattermostHandlerHandleUnsupportedCommandIsSafe(t *testing.T) {
	h := &mattermostInboundHandler{logger: zerolog.Nop()}

	// Without an actor dispatcher the unknown-command reply cannot be delivered,
	// but the call must not panic.
	if err := h.HandleUnsupportedCommand(context.Background(), mattermost.InboundCommand{
		Locator:  mattermost.NewDMLocator("dm-1", "user-1"),
		PostID:   "post-1",
		SenderID: "user-1",
		Command:  "nonsense",
		Direct:   true,
	}); err == nil {
		t.Fatal("HandleUnsupportedCommand() error = nil, want an error when no dispatcher is wired")
	}
}

func TestMattermostHandlerProcessInboundWithoutComponentsRejectsUnauthorizedSender(t *testing.T) {
	h := &mattermostInboundHandler{logger: zerolog.Nop()}

	settlement, err := h.ProcessInbound(context.Background(), mattermost.InboundMessage{
		Locator:    mattermost.NewDMLocator("dm-1", "user-1"),
		PostID:     "post-1",
		SenderID:   "user-1",
		Text:       "hello",
		Direct:     true,
		ReceivedAt: time.Now(),
	})
	// An unauthorized sender settles without an error; nothing may panic.
	if err != nil {
		t.Fatalf("ProcessInbound() error = %v, want nil for an unauthorized post", err)
	}
	if settlement.Outcome == "" {
		t.Fatal("ProcessInbound().Outcome is empty, want a settlement outcome")
	}
}

func TestMattermostHandlerProcessInboundUsesCanonicalChatHandlerForQuestionReply(t *testing.T) {
	const ownerID = "owner-user-1"
	chat := &recordingMattermostChatHandler{result: chatapp.Result{
		Settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted, Reason: chatapp.ReasonAccepted},
		Activated:  true,
	}}
	h := &mattermostInboundHandler{
		ownerStore: newOwnerStoreWithMattermostSubject(t, ownerID),
		chat:       chat,
		logger:     zerolog.Nop(),
	}
	receivedAt := time.Date(2026, time.September, 28, 10, 0, 0, 0, time.UTC)
	settlement, err := h.ProcessInbound(context.Background(), mattermost.InboundMessage{
		Locator:    mattermost.NewChannelLocator("team-1", "channel-1", "question-post"),
		PostID:     "reply-post",
		RootID:     "question-post",
		SenderID:   ownerID,
		Text:       "2",
		Direct:     false,
		ReceivedAt: receivedAt,
	})
	if err != nil {
		t.Fatalf("ProcessInbound() error = %v", err)
	}
	if settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("settlement outcome = %q, want %q", settlement.Outcome, turncmd.InboundAccepted)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("HandleChat calls = %d, want 1", len(chat.requests))
	}
	request := chat.requests[0]
	if request.QuestionReply == nil {
		t.Fatal("QuestionReply = nil, want a Mattermost thread reply")
	}
	if got, want := request.QuestionReply.ReplyToMessageID, "question-post"; got != want {
		t.Fatalf("QuestionReply.ReplyToMessageID = %q, want %q", got, want)
	}
	if got, want := request.QuestionReply.User.UserID, auth.MattermostSubject(ownerID); got != want {
		t.Fatalf("QuestionReply.User.UserID = %q, want %q", got, want)
	}
}

func TestMattermostHandlerNilHandlerIsSafe(t *testing.T) {
	var h *mattermostInboundHandler

	if _, err := h.ProcessInbound(context.Background(), mattermost.InboundMessage{}); err == nil {
		t.Fatal("ProcessInbound() error = nil on a nil handler, want an error")
	}
	if err := h.HandleCommand(context.Background(), mattermost.InboundCommand{}); err != nil {
		t.Fatalf("HandleCommand() error = %v on a nil handler, want nil", err)
	}
	if err := h.HandleUnsupportedCommand(context.Background(), mattermost.InboundCommand{}); err != nil {
		t.Fatalf("HandleUnsupportedCommand() error = %v on a nil handler, want nil", err)
	}
}

func TestMattermostHandlerImplementsInboundProcessor(t *testing.T) {
	var _ mattermost.InboundProcessor = (*mattermostInboundHandler)(nil)
}

func TestNewMattermostInboundHandlerWiresCanonicalChatHandler(t *testing.T) {
	chat := &recordingMattermostChatHandler{}
	processor := newMattermostInboundHandler(mattermostInboundHandlerParams{
		Chat:   chat,
		Logger: zerolog.Nop(),
	})

	handler, ok := processor.(*mattermostInboundHandler)
	if !ok {
		t.Fatalf("newMattermostInboundHandler() returned %T, want *mattermostInboundHandler", processor)
	}
	if handler.chat != chat {
		t.Fatal("handler.chat does not contain the supplied canonical chat handler")
	}
}
