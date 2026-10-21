package startupnotifyfx

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/startupnotifyapp"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

func TestLocalResolversReturnPersonalLocators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver startupnotifyapp.DirectMessageResolver
		binding  usercmd.Binding
		wantType string
		wantKey  string
	}{
		{name: "telegram provenance", resolver: telegramResolver{}, binding: usercmd.Binding{Principal: "42", Provenance: "verified;chat_id=42"}, wantType: "telegram", wantKey: "42:0"},
		{name: "telegram legacy fallback", resolver: telegramResolver{}, binding: usercmd.Binding{Principal: "41", Provenance: "legacy-owner"}, wantType: "telegram", wantKey: "41:0"},
		{name: "zulip", resolver: zulipResolver{}, binding: usercmd.Binding{Principal: "101"}, wantType: "zulip", wantKey: "dm:101"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator, err := test.resolver.ResolveDM(t.Context(), test.binding)
			if err != nil {
				t.Fatalf("ResolveDM() error = %v", err)
			}
			if locator.ChannelType != test.wantType || locator.AddressKey != test.wantKey {
				t.Fatalf("ResolveDM() locator = %q:%q, want %q:%q", locator.ChannelType, locator.AddressKey, test.wantType, test.wantKey)
			}
			if scope, err := classifyScope(locator); err != nil || scope != deliverycmd.LocatorScopePersonal {
				t.Fatalf("locator scope = %q, %v, want personal", scope, err)
			}
		})
	}
}

func TestLocalResolversRejectMalformedBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		resolver startupnotifyapp.DirectMessageResolver
		binding  usercmd.Binding
	}{
		{name: "telegram principal", resolver: telegramResolver{}, binding: usercmd.Binding{Principal: "group"}},
		{name: "telegram provenance", resolver: telegramResolver{}, binding: usercmd.Binding{Principal: "41", Provenance: "chat_id=-1"}},
		{name: "telegram mismatched provenance", resolver: telegramResolver{}, binding: usercmd.Binding{Principal: "41", Provenance: "chat_id=42"}},
		{name: "zulip principal", resolver: zulipResolver{}, binding: usercmd.Binding{Principal: "0"}},
		{name: "slack principal", resolver: slackResolver{client: &fakeSlackClient{}}, binding: usercmd.Binding{Principal: "T1"}},
		{name: "mattermost principal", resolver: mattermostResolver{client: &fakeMattermostClient{}, botUserID: "bot"}, binding: usercmd.Binding{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.resolver.ResolveDM(t.Context(), test.binding); err == nil {
				t.Fatal("ResolveDM() error = nil, want malformed binding error")
			}
		})
	}
}

func TestSlackResolverOpensAndValidatesDirectConversation(t *testing.T) {
	t.Parallel()
	client := &fakeSlackClient{conversationID: "D456"}
	locator, err := (slackResolver{client: client}).ResolveDM(t.Context(), usercmd.Binding{Principal: " T123:U789 "})
	if err != nil {
		t.Fatalf("ResolveDM() error = %v", err)
	}
	if client.userID != "U789" || locator.AddressKey != "c:T123:D456" {
		t.Fatalf("ResolveDM() user/locator = %q/%q", client.userID, locator.AddressKey)
	}
	client.conversationID = "C456"
	if _, err := (slackResolver{client: client}).ResolveDM(t.Context(), usercmd.Binding{Principal: "T123:U789"}); err == nil {
		t.Fatal("ResolveDM() error = nil for non-DM conversation")
	}
}

func TestMattermostResolverUsesDirectChannel(t *testing.T) {
	t.Parallel()
	client := &fakeMattermostClient{channel: mattermost.Channel{ID: "dm-1", Type: "D"}}
	locator, err := (mattermostResolver{client: client, botUserID: "bot-1"}).ResolveDM(t.Context(), usercmd.Binding{Principal: "user-1"})
	if err != nil {
		t.Fatalf("ResolveDM() error = %v", err)
	}
	if client.botUserID != "bot-1" || client.otherUserID != "user-1" || locator.AddressKey != "d:dm-1" {
		t.Fatalf("ResolveDM() request/locator = %q/%q/%q", client.botUserID, client.otherUserID, locator.AddressKey)
	}
	client.channel.Type = "O"
	if _, err := (mattermostResolver{client: client, botUserID: "bot-1"}).ResolveDM(t.Context(), usercmd.Binding{Principal: "user-1"}); err == nil {
		t.Fatal("ResolveDM() error = nil for non-DM channel")
	}
}

func TestResolversPropagateProviderFailures(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("provider unavailable")
	if _, err := (slackResolver{client: &fakeSlackClient{err: wantErr}}).ResolveDM(t.Context(), usercmd.Binding{Principal: "T1:U1"}); !errors.Is(err, wantErr) {
		t.Fatalf("Slack ResolveDM() error = %v, want wrapped provider error", err)
	}
	if _, err := (mattermostResolver{client: &fakeMattermostClient{err: wantErr}, botUserID: "bot"}).ResolveDM(t.Context(), usercmd.Binding{Principal: "user"}); !errors.Is(err, wantErr) {
		t.Fatalf("Mattermost ResolveDM() error = %v, want wrapped provider error", err)
	}
}

func TestDisabledResolverMakesNoNetworkCall(t *testing.T) {
	t.Parallel()
	client := &fakeSlackClient{err: errors.New("must not be called")}
	binding := startupnotifyapp.ResolverBinding{ChannelType: slackagent.ChannelType, Enabled: false, Resolver: slackResolver{client: client}}
	service, err := startupnotifyapp.New(fakeUserReader{user: primaryUser(usercmd.Binding{ID: "b1", ChannelType: slackagent.ChannelType, Principal: "T1:U1"})}, &fakeSender{}, []startupnotifyapp.ResolverBinding{binding}, zerolog.Nop())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("OpenConversation() calls = %d, want 0", client.calls)
	}
}

type fakeSlackClient struct {
	conversationID string
	userID         string
	err            error
	calls          int
}

func (c *fakeSlackClient) OpenConversation(_ context.Context, userID string) (string, error) {
	c.calls++
	c.userID = userID
	return c.conversationID, c.err
}

type fakeMattermostClient struct {
	channel     mattermost.Channel
	botUserID   string
	otherUserID string
	err         error
}

func (c *fakeMattermostClient) GetDirectChannel(_ context.Context, botUserID, otherUserID string) (mattermost.Channel, error) {
	c.botUserID, c.otherUserID = botUserID, otherUserID
	return c.channel, c.err
}

type fakeUserReader struct{ user usercmd.User }

func (r fakeUserReader) ListUsers(context.Context, usercmd.PageRequest) (usercmd.UserPage, error) {
	return usercmd.UserPage{Users: []usercmd.User{r.user}}, nil
}

type fakeSender struct{}

func (*fakeSender) SendPlain(context.Context, deliverycmd.Locator, string) error { return nil }

func primaryUser(binding usercmd.Binding) usercmd.User {
	return usercmd.User{Username: usercmd.PrimaryUsername, Primary: true, Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Bindings: []usercmd.Binding{binding}}
}

func classifyScope(locator deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
	switch locator.ChannelType {
	case "telegram":
		return telegram.ClassifyLocatorScope(locator)
	case "zulip":
		return zulip.ClassifyLocatorScope(locator)
	default:
		return "", errors.New("unsupported test locator")
	}
}
