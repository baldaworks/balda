package locatorref

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

// TestParseMattermostChannel covers the channel and thread address keys. This is
// the regression guard for the defect where mattermost was missing from the
// parser's switch, which made /locator fail and excluded the transport from any
// config target that resolves through locatorref.Parse.
func TestParseMattermostChannel(t *testing.T) {
	t.Parallel()

	conversation, err := Parse("mattermost:c:channel-1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if conversation.ChannelType != string(deliverycmd.ChannelTypeMattermost) {
		t.Fatalf("ChannelType = %q, want mattermost", conversation.ChannelType)
	}
	if conversation.AddressKey != "c:channel-1" {
		t.Fatalf("AddressKey = %q, want c:channel-1", conversation.AddressKey)
	}

	thread, err := Parse("mattermost:c:channel-1:root-1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if thread.AddressKey != "c:channel-1:root-1" {
		t.Fatalf("AddressKey = %q, want c:channel-1:root-1", thread.AddressKey)
	}
	if thread.SessionID == conversation.SessionID {
		t.Fatalf("thread and channel parsed to the same session %q", thread.SessionID)
	}
}

// TestParseMattermostDirectAndGroup covers the two direct-conversation keys and
// asserts they address different sessions, which is the whole point of giving
// group DMs their own prefix.
func TestParseMattermostDirectAndGroup(t *testing.T) {
	t.Parallel()

	direct, err := Parse("mattermost:d:dm-channel-1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if direct.AddressKey != "d:dm-channel-1" {
		t.Fatalf("AddressKey = %q, want d:dm-channel-1", direct.AddressKey)
	}

	group, err := Parse("mattermost:g:group-channel-1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if group.AddressKey != "g:group-channel-1" {
		t.Fatalf("AddressKey = %q, want g:group-channel-1", group.AddressKey)
	}
	if direct.SessionID == group.SessionID {
		t.Fatalf("direct and group conversations parsed to the same session %q", direct.SessionID)
	}
}

// TestFormatMattermostRoundTripsThroughParse verifies the public ref form is
// stable for the transport.
func TestFormatMattermostRoundTripsThroughParse(t *testing.T) {
	t.Parallel()

	locator, err := Parse("mattermost:c:channel-1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got, want := Format(locator), "mattermost:c:channel-1"; got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

// TestParseMattermostRejectsMalformedAddressKey verifies a bad key is refused
// rather than silently producing an unusable locator.
func TestParseMattermostRejectsMalformedAddressKey(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{
		"mattermost:x:channel-1",
		"mattermost:c:",
		"mattermost:d:",
		"mattermost:g:",
	} {
		if _, err := Parse(ref); err == nil {
			t.Fatalf("Parse(%q) error = nil, want non-nil", ref)
		}
	}
}
