package mattermost

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func TestNormalizeInboundMapsPostOntoSharedContract(t *testing.T) {
	receivedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	post := Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Message:   "  reply with a single word  ",
	}
	locator := NewChannelLocator("team-1", "channel-1", "")

	normalized := NormalizeInbound(locator, post, "Alice", false, receivedAt)

	if got, want := normalized.ID, turncmd.InboundID("mattermost:post-1"); got != want {
		t.Fatalf("normalized.ID = %q, want %q", got, want)
	}
	if got, want := normalized.ProviderMessageID, "post-1"; got != want {
		t.Fatalf("normalized.ProviderMessageID = %q, want %q", got, want)
	}
	if got, want := normalized.Text, "reply with a single word"; got != want {
		t.Fatalf("normalized.Text = %q, want %q", got, want)
	}
	if got, want := normalized.UserID, "user-1"; got != want {
		t.Fatalf("normalized.UserID = %q, want %q", got, want)
	}
	if got, want := normalized.Source, turncmd.SourceMattermost; got != want {
		t.Fatalf("normalized.Source = %q, want %q", got, want)
	}
	if got, want := normalized.DeliveryFormat, deliveryfmt.DeliveryFormatMarkdown; got != want {
		t.Fatalf("normalized.DeliveryFormat = %q, want %q", got, want)
	}
	if got, want := normalized.ReceivedAt, receivedAt.Format(time.RFC3339); got != want {
		t.Fatalf("normalized.ReceivedAt = %q, want %q", got, want)
	}
	if got, want := normalized.Locator.SessionID, locator.SessionID; got != want {
		t.Fatalf("normalized.Locator.SessionID = %q, want %q", got, want)
	}
}

func TestNormalizeInboundDisablesTypingProgress(t *testing.T) {
	// Mattermost bot accounts have no typing API, so advertising typing progress
	// would only produce a no-op warning on every turn.
	normalized := NormalizeInbound(NewChannelLocator("team-1", "channel-1", ""), Post{ID: "post-1"}, "Alice", false, time.Now())

	if normalized.ProgressPolicy.Typing {
		t.Fatal("normalized.ProgressPolicy.Typing = true, want false for Mattermost")
	}
	if !normalized.ProgressPolicy.PlanUpdates {
		t.Fatal("normalized.ProgressPolicy.PlanUpdates = false, want true")
	}
}

func TestNormalizeInboundWithoutPostIDHasEmptyLogicalID(t *testing.T) {
	normalized := NormalizeInbound(NewChannelLocator("team-1", "channel-1", ""), Post{}, "Alice", false, time.Now())

	if normalized.ID != "" {
		t.Fatalf("normalized.ID = %q, want \"\" when the post has no id", normalized.ID)
	}
	if normalized.ProviderMessageID != "" {
		t.Fatalf("normalized.ProviderMessageID = %q, want \"\"", normalized.ProviderMessageID)
	}
}

func TestBuildInboundReplyMapsMattermostThreadReply(t *testing.T) {
	receivedAt := time.Date(2026, time.September, 28, 10, 0, 0, 0, time.UTC)
	locator := NewChannelLocator("team-1", "channel-1", "question-post")
	reply, ok := BuildInboundReply(locator, "mattermost:user-1", InboundMessage{
		PostID: "reply-post",
		RootID: "question-post",
		Text:   "  second option  ",
	}, receivedAt)
	if !ok {
		t.Fatal("BuildInboundReply() ok = false, want true")
	}
	if got, want := reply.Provider, ChannelType; got != want {
		t.Fatalf("Provider = %q, want %q", got, want)
	}
	if got, want := reply.ReplyToMessageID, "question-post"; got != want {
		t.Fatalf("ReplyToMessageID = %q, want %q", got, want)
	}
	if got, want := reply.MessageID, "reply-post"; got != want {
		t.Fatalf("MessageID = %q, want %q", got, want)
	}
	if got, want := reply.Text, "second option"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

func TestLocatorForPostUsesChannelForTeamChannel(t *testing.T) {
	channel := Channel{ID: "channel-1", TeamID: "team-1", Type: channelTypeOpen}
	post := Post{ID: "post-1", ChannelID: "channel-1", UserID: "user-1"}

	locator := LocatorForPost(channel, post)

	if got := ChannelIDOf(locator); got != "channel-1" {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, "channel-1")
	}
	if IsDirectLocator(locator) {
		t.Fatal("IsDirectLocator() = true, want false for a team channel")
	}
}

func TestLocatorForPostScopesReplyToThreadRoot(t *testing.T) {
	channel := Channel{ID: "channel-1", TeamID: "team-1", Type: channelTypeOpen}
	post := Post{ID: "post-2", ChannelID: "channel-1", UserID: "user-1", RootID: "post-1"}

	locator := LocatorForPost(channel, post)

	rootID, ok := RootIDFromLocator(locator)
	if !ok || rootID != "post-1" {
		t.Fatalf("RootIDFromLocator() = (%q, %v), want (%q, true)", rootID, ok, "post-1")
	}

	// A thread is its own conversation: every reply in it must resolve to one
	// session, distinct from the channel-level session, so the whole thread
	// accumulates in a single Balda session.
	sibling := LocatorForPost(channel, Post{ID: "post-3", ChannelID: "channel-1", UserID: "user-2", RootID: "post-1"})
	if sibling.SessionID != locator.SessionID {
		t.Fatalf("two replies in one thread resolved to different sessions: %q != %q", sibling.SessionID, locator.SessionID)
	}

	channelLocator := LocatorForPost(channel, Post{ID: "post-1", ChannelID: "channel-1", UserID: "user-1"})
	if channelLocator.SessionID == locator.SessionID {
		t.Fatalf("thread session %q collides with the channel session %q", locator.SessionID, channelLocator.SessionID)
	}
	if got, want := ChannelIDOf(locator), ChannelIDOf(channelLocator); got != want {
		t.Fatalf("thread and channel locators resolve to different channels: %q vs %q", got, want)
	}
}

func TestLocatorForPostUsesDirectLocatorForDMAndGroupChannels(t *testing.T) {
	for _, channelType := range []string{channelTypeDirect, channelTypeGroup} {
		t.Run(channelType, func(t *testing.T) {
			channel := Channel{ID: "dm-channel-1", Type: channelType}
			post := Post{ID: "post-1", ChannelID: "dm-channel-1", UserID: "user-1"}

			locator := LocatorForPost(channel, post)

			if !IsDirectLocator(locator) {
				t.Fatalf("IsDirectLocator() = false, want true for channel type %q", channelType)
			}
			if got := ChannelIDOf(locator); got != "dm-channel-1" {
				t.Fatalf("ChannelIDOf() = %q, want %q", got, "dm-channel-1")
			}
		})
	}
}

// TestGroupChannelPostsFromTwoSendersShareOneSession guards the group-DM session
// identity. A group channel is one conversation shared by several participants,
// so the session must be derived from the channel and never from the sender: an
// author-keyed session would silently shard one chat into a session per person.
func TestGroupChannelPostsFromTwoSendersShareOneSession(t *testing.T) {
	channel := Channel{ID: testChannelID, Type: channelTypeGroup}

	first := LocatorForPost(channel, Post{ID: "post-1", ChannelID: testChannelID, UserID: testUserID})
	second := LocatorForPost(channel, Post{ID: "post-2", ChannelID: testChannelID, UserID: "user-2"})

	if first.SessionID != second.SessionID {
		t.Fatalf("two senders in one group channel resolved to different sessions: %q != %q", first.SessionID, second.SessionID)
	}
	if first.AddressKey != second.AddressKey {
		t.Fatalf("two senders in one group channel resolved to different address keys: %q != %q", first.AddressKey, second.AddressKey)
	}
	if !IsDirectLocator(first) || !IsDirectLocator(second) {
		t.Fatal("group channel locators must report as direct conversations")
	}
	if got := ChannelIDOf(first); got != testChannelID {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, testChannelID)
	}
}

// TestGroupChannelSessionIsDistinctFromDirectAndChannel pins the group session
// apart from the 1:1 direct session and from a team channel with the same id, so
// a group conversation can never collide with either.
func TestGroupChannelSessionIsDistinctFromDirectAndChannel(t *testing.T) {
	group := LocatorForPost(Channel{ID: testChannelID, Type: channelTypeGroup}, Post{ChannelID: testChannelID, UserID: testUserID})
	direct := LocatorForPost(Channel{ID: testChannelID, Type: channelTypeDirect}, Post{ChannelID: testChannelID, UserID: testUserID})
	team := LocatorForPost(Channel{ID: testChannelID, Type: channelTypeOpen}, Post{ChannelID: testChannelID, UserID: testUserID})

	if group.SessionID == direct.SessionID {
		t.Fatalf("group and direct sessions collided on %q", group.SessionID)
	}
	if group.SessionID == team.SessionID {
		t.Fatalf("group and channel sessions collided on %q", group.SessionID)
	}
	// The same group conversation must also carry a distinct address key, so a
	// locator rebuilt from a config ref cannot land on the wrong conversation.
	if group.AddressKey == direct.AddressKey {
		t.Fatalf("group and direct address keys collided on %q", group.AddressKey)
	}
}

// TestGroupChannelLocatorRoundTripsThroughAddressKey verifies the group identity
// survives a rebuild from its address key, which is how scheduler and webhook
// targets re-enter the conversation.
func TestGroupChannelLocatorRoundTripsThroughAddressKey(t *testing.T) {
	group := LocatorForPost(Channel{ID: testChannelID, Type: channelTypeGroup}, Post{ChannelID: testChannelID, UserID: testUserID})

	rebuilt, err := LocatorFromAddressKey(group.AddressKey)
	if err != nil {
		t.Fatalf("LocatorFromAddressKey(%q) error = %v", group.AddressKey, err)
	}
	if rebuilt.SessionID != group.SessionID {
		t.Fatalf("rebuilt session %q != original %q", rebuilt.SessionID, group.SessionID)
	}
	if !IsDirectLocator(rebuilt) {
		t.Fatal("rebuilt group locator must still report as a direct conversation")
	}
}

func TestLocatorForPostFallsBackToChannelIDWhenPostOmitsIt(t *testing.T) {
	channel := Channel{ID: "channel-1", TeamID: "team-1", Type: channelTypePrivate}
	post := Post{ID: "post-1", UserID: "user-1"}

	locator := LocatorForPost(channel, post)

	if got := ChannelIDOf(locator); got != "channel-1" {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, "channel-1")
	}
}

func TestIsDirectChannelType(t *testing.T) {
	cases := map[string]bool{
		channelTypeDirect:  true,
		channelTypeGroup:   true,
		channelTypeOpen:    false,
		channelTypePrivate: false,
		"":                 false,
	}
	for input, want := range cases {
		if got := IsDirectChannelType(input); got != want {
			t.Fatalf("IsDirectChannelType(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestIsBotEcho(t *testing.T) {
	bot := Post{UserID: "bot-1"}
	human := Post{UserID: "user-1"}

	if !IsBotEcho(bot, "bot-1") {
		t.Fatal("IsBotEcho(bot) = false, want true")
	}
	if IsBotEcho(human, "bot-1") {
		t.Fatal("IsBotEcho(human) = true, want false")
	}
	// Without a configured bot id nothing can be classified as echo.
	if IsBotEcho(bot, "") {
		t.Fatal("IsBotEcho() = true with an empty bot id, want false")
	}
}

func TestIsSystemPostAndIsDeletedPost(t *testing.T) {
	if !IsSystemPost(Post{Type: "system_join_channel"}) {
		t.Fatal("IsSystemPost(system) = false, want true")
	}
	if IsSystemPost(Post{Type: ""}) {
		t.Fatal("IsSystemPost(user post) = true, want false")
	}
	if !IsDeletedPost(Post{DeleteAt: 1}) {
		t.Fatal("IsDeletedPost(deleted) = false, want true")
	}
	if IsDeletedPost(Post{DeleteAt: 0}) {
		t.Fatal("IsDeletedPost(live) = true, want false")
	}
}

func TestStripMentionRemovesOnlyStandaloneBotMention(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "leading mention", text: "@balda reply with a single word", want: "reply with a single word"},
		{name: "mention with punctuation", text: "@balda: hello", want: ": hello"},
		{name: "mention only", text: "@balda", want: ""},
		{name: "no mention", text: "please reply", want: "please reply"},
		{name: "longer username is not the bot", text: "@baldabravo hello", want: "@baldabravo hello"},
		{name: "mention is not at the start", text: "hi @balda", want: "hi @balda"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripMention(tc.text, "balda"); got != tc.want {
				t.Fatalf("StripMention(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestStripMentionWithoutBotUsernameIsPassthrough(t *testing.T) {
	if got := StripMention("@balda hello", ""); got != "@balda hello" {
		t.Fatalf("StripMention() = %q, want the input unchanged", got)
	}
}

func TestMentionsBot(t *testing.T) {
	if !MentionsBot("@balda hello", "balda") {
		t.Fatal("MentionsBot(@balda hello) = false, want true")
	}
	if !MentionsBot("@BALDA hello", "balda") {
		t.Fatal("MentionsBot() = false, want true for a case-insensitive mention")
	}
	if MentionsBot("hello", "balda") {
		t.Fatal("MentionsBot(hello) = true, want false")
	}
	if MentionsBot("@balda hello", "") {
		t.Fatal("MentionsBot() = true with an empty bot username, want false")
	}
	for _, text := range []string{"@baldabot hello", "@balda-team hello", "person@balda.example"} {
		if MentionsBot(text, "balda") {
			t.Fatalf("MentionsBot(%q) = true for a longer identifier, want false", text)
		}
	}
	if !MentionsBot("hello, (@balda) please reply", "balda") {
		t.Fatal("MentionsBot() = false for a standalone mention in punctuation")
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "plain command", text: "/topic ops", wantName: "topic", wantArgs: "ops", wantOK: true},
		{name: "command without args", text: "/usage", wantName: "usage", wantArgs: "", wantOK: true},
		{name: "uppercase is normalised", text: "/TOPIC ops", wantName: "topic", wantArgs: "ops", wantOK: true},
		{name: "extra whitespace", text: "  /topic   ops  ", wantName: "topic", wantArgs: "ops", wantOK: true},
		{name: "user invite normalises to add", text: "/user invite alice", wantName: "user", wantArgs: "add alice", wantOK: true},
		{name: "not a command", text: "hello", wantOK: false},
		{name: "bare slash", text: "/", wantOK: false},
		{name: "empty", text: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, args, ok := ParseCommand(tc.text)
			if ok != tc.wantOK {
				t.Fatalf("ParseCommand(%q) ok = %v, want %v", tc.text, ok, tc.wantOK)
			}
			if name != tc.wantName {
				t.Fatalf("ParseCommand(%q) name = %q, want %q", tc.text, name, tc.wantName)
			}
			if args != tc.wantArgs {
				t.Fatalf("ParseCommand(%q) args = %q, want %q", tc.text, args, tc.wantArgs)
			}
		})
	}
}

func TestPostIDFromWebSocket(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want string
	}{
		{name: "string", raw: "post-1", want: "post-1"},
		{name: "string with spaces", raw: " post-1 ", want: "post-1"},
		{name: "float64", raw: float64(42), want: "42"},
		{name: "int64", raw: int64(42), want: "42"},
		{name: "int", raw: 42, want: "42"},
		{name: "unsupported", raw: true, want: ""},
		{name: "nil", raw: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PostIDFromWebSocket(tc.raw); got != tc.want {
				t.Fatalf("PostIDFromWebSocket(%v) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParsePostIDIsNotUsedForMattermostIdentifiers(t *testing.T) {
	// Mattermost post ids are opaque 26-char strings, so the numeric field is
	// always 0; correlation must go through ProviderMessageID instead.
	if got := ParsePostID("dqagqce7pibiipt3qtqnkgfd8c"); got != 0 {
		t.Fatalf("ParsePostID() = %d, want 0 for an opaque post id", got)
	}
	if got := ParsePostID("42"); got != 42 {
		t.Fatalf("ParsePostID(42) = %d, want 42", got)
	}
}
