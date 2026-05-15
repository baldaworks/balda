package telegram

import (
	"strings"
	"testing"

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

func TestUserID(t *testing.T) {
	if got := UserID(101); got != "tg-101" {
		t.Fatalf("UserID() = %q, want %q", got, "tg-101")
	}
}
