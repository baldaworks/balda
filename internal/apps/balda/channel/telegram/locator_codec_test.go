package telegram

import (
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

func TestNewLocator_RoundTripDecode(t *testing.T) {
	locator := NewLocator(9001, 77)

	if locator.ChannelType != baldastate.ChannelTypeTelegram {
		t.Fatalf("ChannelType = %q, want %q", locator.ChannelType, baldastate.ChannelTypeTelegram)
	}
	if locator.AddressKey != "9001:77" {
		t.Fatalf("AddressKey = %q, want %q", locator.AddressKey, "9001:77")
	}
	if locator.SessionID != "tg-9001-77" {
		t.Fatalf("SessionID = %q, want %q", locator.SessionID, "tg-9001-77")
	}

	address, ok, err := DecodeLocator(locator)
	if err != nil {
		t.Fatalf("DecodeLocator() error = %v", err)
	}
	if !ok {
		t.Fatal("DecodeLocator() ok = false, want true")
	}
	if address.ChatID != 9001 || address.TopicID != 77 {
		t.Fatalf("DecodeLocator() = %+v, want chat/topic 9001/77", address)
	}
}

func TestDecodeLocator_NonTelegram(t *testing.T) {
	locator, err := baldasession.NewSessionLocator("slack", "team:42", `{"channel":"ops"}`, "slack-42")
	if err != nil {
		t.Fatalf("NewSessionLocator() error = %v", err)
	}

	_, ok, err := DecodeLocator(locator)
	if err != nil {
		t.Fatalf("DecodeLocator() error = %v, want nil", err)
	}
	if ok {
		t.Fatal("DecodeLocator() ok = true, want false for non-telegram locator")
	}
}

func TestDecodeLocator_InvalidTelegramAddressJSON(t *testing.T) {
	locator, err := baldasession.NewSessionLocator(baldastate.ChannelTypeTelegram, "1:2", "{", "tg-1-2")
	if err != nil {
		t.Fatalf("NewSessionLocator() error = %v", err)
	}

	_, ok, err := DecodeLocator(locator)
	if !ok {
		t.Fatal("DecodeLocator() ok = false, want true for telegram channel type")
	}
	if err == nil {
		t.Fatal("DecodeLocator() error = nil, want decode error")
	}
	if !strings.Contains(err.Error(), "decode telegram address") {
		t.Fatalf("DecodeLocator() error = %q, want decode telegram address context", err.Error())
	}
}

func TestLocatorFromAddressKey(t *testing.T) {
	locator, err := LocatorFromAddressKey("-1002667079342:8939")
	if err != nil {
		t.Fatalf("LocatorFromAddressKey() error = %v", err)
	}
	if got, want := locator, NewLocator(-1002667079342, 8939); got != want {
		t.Fatalf("LocatorFromAddressKey() = %+v, want %+v", got, want)
	}
}

func TestLocatorFromAddressKey_Invalid(t *testing.T) {
	_, err := LocatorFromAddressKey("oops")
	if err == nil {
		t.Fatal("LocatorFromAddressKey() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "<chat_id>:<topic_id>") {
		t.Fatalf("LocatorFromAddressKey() error = %v", err)
	}
}

func TestUserID(t *testing.T) {
	if got := UserID(101); got != "tg-101" {
		t.Fatalf("UserID() = %q, want %q", got, "tg-101")
	}
}

func TestClassifyLocatorScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		locator deliverycmd.Locator
		want    deliverycmd.LocatorScopeKind
	}{
		{name: "personal", locator: NewLocator(101, 0), want: deliverycmd.LocatorScopePersonal},
		{name: "personal topic", locator: NewLocator(101, 77), want: deliverycmd.LocatorScopePersonal},
		{name: "group topic", locator: NewLocator(-1001, 77), want: deliverycmd.LocatorScopeGroup},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyLocatorScope(test.locator)
			if err != nil {
				t.Fatalf("ClassifyLocatorScope() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ClassifyLocatorScope() = %q, want %q", got, test.want)
			}
		})
	}
}
