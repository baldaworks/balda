package handlersfx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
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

func TestMattermostHandlerAuthorizeInboundAllowsOwnerOnly(t *testing.T) {
	const ownerID = "owner-user-1"
	ownerStore := newOwnerStoreWithMattermostSubject(t, ownerID)
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	allowed, err := h.authorizeInbound(context.Background(), ingressInboundContext(ownerID))
	if err != nil {
		t.Fatalf("authorizeInbound(owner) error = %v", err)
	}
	if !allowed.Allowed {
		t.Fatal("authorizeInbound(owner).Allowed = false, want true")
	}

	denied, err := h.authorizeInbound(context.Background(), ingressInboundContext("stranger-1"))
	if err != nil {
		t.Fatalf("authorizeInbound(stranger) error = %v", err)
	}
	if denied.Allowed {
		t.Fatal("authorizeInbound(stranger).Allowed = true, want false")
	}
}

func TestMattermostHandlerAuthorizeInboundRejectsEmptyUser(t *testing.T) {
	ownerStore := newOwnerStoreWithMattermostSubject(t, "owner-user-1")
	h := &mattermostInboundHandler{ownerStore: ownerStore, logger: zerolog.Nop()}

	result, err := h.authorizeInbound(context.Background(), ingressInboundContext(""))
	if err != nil {
		t.Fatalf("authorizeInbound(empty) error = %v", err)
	}
	if result.Allowed {
		t.Fatal("authorizeInbound(empty).Allowed = true, want false")
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
	if result, err := h.authorizeInbound(context.Background(), ingressInboundContext("user-1")); err != nil {
		t.Fatalf("authorizeInbound() error = %v", err)
	} else if result.Allowed {
		t.Fatal("authorizeInbound().Allowed = true without an owner store, want false")
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

func TestMattermostHandlerPrepareSessionWithoutManagerIsNotReady(t *testing.T) {
	h := &mattermostInboundHandler{logger: zerolog.Nop()}

	preparation, err := h.prepareSession(context.Background(), ingressInboundContext("user-1"))
	if err != nil {
		t.Fatalf("prepareSession() error = %v", err)
	}
	if preparation.Ready {
		t.Fatal("prepareSession().Ready = true without a session manager, want false")
	}
	if got, want := preparation.Reason, mattermostIngressReasonSessionUnavailable; got != want {
		t.Fatalf("prepareSession().Reason = %q, want %q", got, want)
	}
}

func TestMattermostHandlerProcessInboundWithoutComponentsFailsSafely(t *testing.T) {
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

func TestNewMattermostInboundHandlerTrimsConfiguredValues(t *testing.T) {
	processor := newMattermostInboundHandler(mattermostInboundHandlerParams{
		AuthToken:       "  auth-token  ",
		BaldaProviderID: "  deepseek  ",
		Logger:          zerolog.Nop(),
	})

	handler, ok := processor.(*mattermostInboundHandler)
	if !ok {
		t.Fatalf("newMattermostInboundHandler() returned %T, want *mattermostInboundHandler", processor)
	}
	if got, want := handler.authToken, "auth-token"; got != want {
		t.Fatalf("authToken = %q, want %q", got, want)
	}
	if got, want := handler.baldaProviderName, "deepseek"; got != want {
		t.Fatalf("baldaProviderName = %q, want %q", got, want)
	}
}

func ingressInboundContext(userID string) ingressapp.InboundContext {
	return ingressapp.InboundContext{
		ChannelType: mattermost.ChannelType,
		AddressKey:  "d:dm-1",
		AddressJSON: `{"type":"dm","channel_id":"dm-1","user_id":"` + userID + `"}`,
		SessionID:   "mm-dm-1",
		UserID:      userID,
	}
}
