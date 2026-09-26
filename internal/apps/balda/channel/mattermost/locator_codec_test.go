package mattermost

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

// Shared sample values for locator tests. They are constants so repeated string
// literals do not drift between assertions.
const (
	testTeamID      = "team-1"
	testChannelID   = "channel-1"
	testDMChannelID = "dm-channel-1"
	testUserID      = "user-1"
)

func TestNewChannelLocatorRoundTripsThroughDecode(t *testing.T) {
	locator := NewChannelLocator(testTeamID, testChannelID, "")

	if got, want := locator.ChannelType, string(deliverycmd.ChannelTypeMattermost); got != want {
		t.Fatalf("locator.ChannelType = %q, want %q", got, want)
	}
	if got, want := locator.AddressKey, "c:channel-1"; got != want {
		t.Fatalf("locator.AddressKey = %q, want %q", got, want)
	}

	address, ok, err := DecodeLocator(locator)
	if err != nil {
		t.Fatalf("DecodeLocator() error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeLocator() ok = false, want true")
	}
	if got, want := address.Type, addressTypeChannel; got != want {
		t.Fatalf("address.Type = %q, want %q", got, want)
	}
	if got, want := address.ChannelID, testChannelID; got != want {
		t.Fatalf("address.ChannelID = %q, want %q", got, want)
	}
	if got, want := address.TeamID, testTeamID; got != want {
		t.Fatalf("address.TeamID = %q, want %q", got, want)
	}

	if got := ChannelIDOf(locator); got != testChannelID {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, testChannelID)
	}
}

func TestNewChannelLocatorScopesThreadToRootPost(t *testing.T) {
	channelLocator := NewChannelLocator(testTeamID, testChannelID, "")
	threadLocator := NewChannelLocator(testTeamID, testChannelID, "root-9")

	// A thread must stay inside the same channel but be its own conversation:
	// the address key gains the root id and the session id differs.
	if got, want := threadLocator.AddressKey, "c:channel-1:root-9"; got != want {
		t.Fatalf("thread AddressKey = %q, want %q", got, want)
	}
	if threadLocator.SessionID == channelLocator.SessionID {
		t.Fatalf("thread SessionID = channel SessionID = %q, want distinct", threadLocator.SessionID)
	}
	if got := ChannelIDOf(threadLocator); got != testChannelID {
		t.Fatalf("ChannelIDOf(thread) = %q, want %q", got, testChannelID)
	}
	if rootID, ok := RootIDFromLocator(threadLocator); !ok || rootID != "root-9" {
		t.Fatalf("RootIDFromLocator(thread) = (%q, %v), want (%q, true)", rootID, ok, "root-9")
	}

	// A second locator for the same channel+root must be stable, so replies in
	// one thread map to a single session.
	rebuilt := NewChannelLocator(testTeamID, testChannelID, "root-9")
	if rebuilt.SessionID != threadLocator.SessionID {
		t.Fatalf("thread session id is not stable: %q != %q", rebuilt.SessionID, threadLocator.SessionID)
	}
	if rebuilt.AddressKey != threadLocator.AddressKey {
		t.Fatalf("thread address key is not stable: %q != %q", rebuilt.AddressKey, threadLocator.AddressKey)
	}
}

func TestNewDMLocatorRoundTripsThroughDecode(t *testing.T) {
	locator := NewDMLocator(testDMChannelID, testUserID)

	if got, want := locator.AddressKey, "d:dm-channel-1"; got != want {
		t.Fatalf("locator.AddressKey = %q, want %q", got, want)
	}

	address, ok, err := DecodeLocator(locator)
	if err != nil {
		t.Fatalf("DecodeLocator() error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeLocator() ok = false, want true")
	}
	if got, want := address.Type, addressTypeDM; got != want {
		t.Fatalf("address.Type = %q, want %q", got, want)
	}
	if got, want := address.UserID, testUserID; got != want {
		t.Fatalf("address.UserID = %q, want %q", got, want)
	}
	if !IsDirectLocator(locator) {
		t.Fatal("IsDirectLocator() = false, want true")
	}
	if IsDirectLocator(NewChannelLocator(testTeamID, testChannelID, "")) {
		t.Fatal("IsDirectLocator(channel) = true, want false")
	}
}

func TestDecodeLocatorRejectsForeignChannelType(t *testing.T) {
	locator := deliverycmd.Locator{
		ChannelType: "zulip",
		AddressKey:  "s:1:ops",
		AddressJSON: `{"type":"channel","channel_id":"channel-1"}`,
		SessionID:   "zu-s-1-abc",
	}

	_, ok, err := DecodeLocator(locator)
	if err != nil {
		t.Fatalf("DecodeLocator() error = %v, want nil for a foreign transport", err)
	}
	if ok {
		t.Fatal("DecodeLocator() ok = true, want false for a foreign transport")
	}
}

func TestDecodeLocatorRejectsMalformedAddress(t *testing.T) {
	locator := deliverycmd.Locator{
		ChannelType: string(deliverycmd.ChannelTypeMattermost),
		AddressKey:  "c:channel-1",
		AddressJSON: `{`,
		SessionID:   "mm-c-1",
	}

	if _, _, err := DecodeLocator(locator); err == nil {
		t.Fatal("DecodeLocator() error = nil, want a decode error")
	}
}

func TestDecodeLocatorRejectsChannelAddressWithoutChannelID(t *testing.T) {
	locator := deliverycmd.Locator{
		ChannelType: string(deliverycmd.ChannelTypeMattermost),
		AddressKey:  "c:",
		AddressJSON: `{"type":"channel"}`,
		SessionID:   "mm-c-1",
	}

	_, ok, err := DecodeLocator(locator)
	if !ok {
		t.Fatal("DecodeLocator() ok = false, want true for a Mattermost locator")
	}
	if err == nil {
		t.Fatal("DecodeLocator() error = nil, want an error for a channel address without channel_id")
	}
}

func TestChannelIDOfToleratesMissingOrInvalidAddress(t *testing.T) {
	if got := ChannelIDOf(deliverycmd.Locator{}); got != "" {
		t.Fatalf("ChannelIDOf(empty) = %q, want \"\"", got)
	}
	if got := ChannelIDOf(deliverycmd.Locator{AddressJSON: "not-json"}); got != "" {
		t.Fatalf("ChannelIDOf(invalid) = %q, want \"\"", got)
	}
}

func TestLocatorFromAddressKeyRebuildsChannelLocator(t *testing.T) {
	locator, err := LocatorFromAddressKey("c:channel-1")
	if err != nil {
		t.Fatalf("LocatorFromAddressKey() error = %v", err)
	}
	if got, want := locator.ChannelType, string(deliverycmd.ChannelTypeMattermost); got != want {
		t.Fatalf("locator.ChannelType = %q, want %q", got, want)
	}
	if got := ChannelIDOf(locator); got != testChannelID {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, testChannelID)
	}
	if rootID, ok := RootIDFromLocator(locator); !ok || rootID != "" {
		t.Fatalf("RootIDFromLocator() = (%q, %v), want (\"\", true)", rootID, ok)
	}
}

func TestLocatorFromAddressKeyRebuildsThreadLocator(t *testing.T) {
	locator, err := LocatorFromAddressKey("c:channel-1:root-9")
	if err != nil {
		t.Fatalf("LocatorFromAddressKey() error = %v", err)
	}
	if got := ChannelIDOf(locator); got != testChannelID {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, testChannelID)
	}
	if rootID, ok := RootIDFromLocator(locator); !ok || rootID != "root-9" {
		t.Fatalf("RootIDFromLocator() = (%q, %v), want (%q, true)", rootID, ok, "root-9")
	}
}

func TestLocatorFromAddressKeyRebuildsDMLocator(t *testing.T) {
	locator, err := LocatorFromAddressKey("d:dm-channel-1")
	if err != nil {
		t.Fatalf("LocatorFromAddressKey() error = %v", err)
	}
	if got := ChannelIDOf(locator); got != testDMChannelID {
		t.Fatalf("ChannelIDOf() = %q, want %q", got, testDMChannelID)
	}
	if !IsDirectLocator(locator) {
		t.Fatal("IsDirectLocator() = false, want true")
	}
}

func TestLocatorFromAddressKeyRejectsUnknownPrefix(t *testing.T) {
	if _, err := LocatorFromAddressKey("x:channel-1"); err == nil {
		t.Fatal("LocatorFromAddressKey() error = nil, want an error for an unknown prefix")
	}
	if _, err := LocatorFromAddressKey("c:"); err == nil {
		t.Fatal("LocatorFromAddressKey() error = nil, want an error for an empty channel id")
	}
	if _, err := LocatorFromAddressKey("d:"); err == nil {
		t.Fatal("LocatorFromAddressKey() error = nil, want an error for an empty dm channel id")
	}
}

func TestClassifyLocatorScopeSeparatesChannelFromDirect(t *testing.T) {
	channelScope, err := ClassifyLocatorScope(NewChannelLocator(testTeamID, testChannelID, ""))
	if err != nil {
		t.Fatalf("ClassifyLocatorScope(channel) error = %v", err)
	}
	if channelScope != deliverycmd.LocatorScopeGroup {
		t.Fatalf("ClassifyLocatorScope(channel) = %q, want %q", channelScope, deliverycmd.LocatorScopeGroup)
	}

	dmScope, err := ClassifyLocatorScope(NewDMLocator(testDMChannelID, testUserID))
	if err != nil {
		t.Fatalf("ClassifyLocatorScope(dm) error = %v", err)
	}
	if dmScope != deliverycmd.LocatorScopePersonal {
		t.Fatalf("ClassifyLocatorScope(dm) = %q, want %q", dmScope, deliverycmd.LocatorScopePersonal)
	}
}

func TestClassifyLocatorScopeRejectsForeignLocator(t *testing.T) {
	if _, err := ClassifyLocatorScope(deliverycmd.Locator{ChannelType: "zulip"}); err == nil {
		t.Fatal("ClassifyLocatorScope(zulip) error = nil, want an error")
	}
}

func TestUserIDRoundTripsThroughParseUserID(t *testing.T) {
	encoded := UserID("abc123")
	if got, want := encoded, "mm-abc123"; got != want {
		t.Fatalf("UserID() = %q, want %q", got, want)
	}

	decoded, err := ParseUserID(encoded)
	if err != nil {
		t.Fatalf("ParseUserID() error = %v", err)
	}
	if got, want := decoded, "abc123"; got != want {
		t.Fatalf("ParseUserID() = %q, want %q", got, want)
	}

	if _, err := ParseUserID("abc123"); err == nil {
		t.Fatal("ParseUserID() error = nil, want an error without the mm- prefix")
	}
	if _, err := ParseUserID("mm-"); err == nil {
		t.Fatal("ParseUserID() error = nil, want an error for an empty id")
	}
}

func TestChannelAndDirectLocatorsNeverCollide(t *testing.T) {
	// A channel and a direct conversation can share an id space, so the address
	// keys must differ to keep sessions isolated.
	channel := NewChannelLocator(testTeamID, "shared", "")
	direct := NewDMLocator("shared", testUserID)

	if channel.AddressKey == direct.AddressKey {
		t.Fatalf("channel and dm address keys collide: %q", channel.AddressKey)
	}
	if channel.SessionID == direct.SessionID {
		t.Fatalf("channel and dm session ids collide: %q", channel.SessionID)
	}
}
