package turncmd

import (
	"testing"

	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

func TestSessionTurnEnvelopeCarriesGeneratedDedupeKeyInPayload(t *testing.T) {
	t.Parallel()

	env, err := SessionTurnEnvelope(SessionTurnPayload{
		Text: "test",
		Locator: baldasession.SessionLocator{
			SessionID: "tg-1-0",
		},
	})
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	if env.DedupeKey == "" {
		t.Fatal("envelope dedupe key is empty")
	}
	if env.DedupeKey != env.ID {
		t.Fatalf("generated dedupe key = %q, want envelope ID %q", env.DedupeKey, env.ID)
	}

	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.DedupeKey != env.DedupeKey {
		t.Fatalf("payload dedupe key = %q, want %q", payload.DedupeKey, env.DedupeKey)
	}
}

func TestSessionTurnEnvelopePreservesExplicitDedupeKey(t *testing.T) {
	t.Parallel()

	env, err := SessionTurnEnvelope(SessionTurnPayload{
		Text:      "test",
		DedupeKey: "transport-message-1",
		Locator: baldasession.SessionLocator{
			SessionID: "tg-1-0",
		},
	})
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	if env.DedupeKey != "transport-message-1" {
		t.Fatalf("dedupe key = %q, want transport-message-1", env.DedupeKey)
	}
}

func TestNormalizedInboundCarriesOneOrderedAttachmentSetThroughDurableTurn(t *testing.T) {
	t.Parallel()

	const firstFileID = "first"
	inbound := NormalizedInbound{
		ID:   " telegram:9001:media-group-17 ",
		Text: "two files",
		Attachments: []attachment.Descriptor{
			{Kind: attachment.KindDocument, FileID: firstFileID, FileName: "a.txt"},
			{Kind: attachment.KindDocument, FileID: "second", FileName: "b.txt"},
		},
		Locator: baldasession.SessionLocator{
			SessionID:   "tg-9001-77",
			ChannelType: deliveryfmt.TransportTelegram,
			AddressKey:  "9001:77",
		},
		ProviderMessageID: "17",
		UserID:            "101",
		ReceivedAt:        "2026-08-04T09:00:00Z",
		DeliveryFormat:    deliveryfmt.DeliveryFormatRichMarkdown,
		Source:            SourceTelegram,
	}

	payload, err := inbound.SessionTurn()
	if err != nil {
		t.Fatalf("SessionTurn() error = %v", err)
	}
	if payload.DedupeKey != "telegram:9001:media-group-17" {
		t.Fatalf("dedupe key = %q, want stable logical inbound ID", payload.DedupeKey)
	}
	if !payload.Deliver {
		t.Fatal("normalized inbound turn deliver = false, want user-facing delivery")
	}
	if len(payload.Attachments) != 2 || payload.Attachments[0].FileID != firstFileID || payload.Attachments[1].FileID != "second" {
		t.Fatalf("attachments = %+v, want original order", payload.Attachments)
	}

	inbound.Attachments[0].FileID = "changed-after-conversion"
	if payload.Attachments[0].FileID != firstFileID {
		t.Fatalf("payload attachment changed through source alias: %+v", payload.Attachments[0])
	}

	env, err := SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	var durable SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &durable); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if durable.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q, want %q", durable.DeliveryFormat, deliveryfmt.DeliveryFormatRichMarkdown)
	}
	if len(durable.Attachments) != 2 || durable.Attachments[0].FileID != firstFileID || durable.Attachments[1].FileID != "second" {
		t.Fatalf("durable attachments = %+v, want original order", durable.Attachments)
	}
}
