package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestTechnicalProgressDirectChatUsesOrdinaryMessage(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "")
	message := deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichMarkdown,
		Text:          "**working**",
		PlainFallback: "working",
	}

	err := adapter.SendProgressMessage(context.Background(), NewLocator(101, 0), technicalProgress("tg-101"), &message)
	if err != nil {
		t.Fatalf("SendProgressMessage() error = %v", err)
	}
	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich messages = %d, want 1", len(tgClient.richMessages))
	}
	if tgClient.richMessages[0].EphemeralMessageParameters != nil {
		t.Fatal("direct-chat technical progress must not be ephemeral")
	}
}

func TestTechnicalProgressGroupUsesEphemeralPlainMessage(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "")
	message := deliveryfmt.Message{Name: deliveryfmt.NamePlainText, Text: "working", PlainFallback: "working"}

	err := adapter.SendProgressMessage(context.Background(), NewLocator(-1001, 0), technicalProgress("tg-101"), &message)
	if err != nil {
		t.Fatalf("SendProgressMessage() error = %v", err)
	}
	if len(tgClient.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(tgClient.messages))
	}
	request := tgClient.messages[0]
	if request.EphemeralMessageParameters == nil || request.EphemeralMessageParameters.ReceiverUserId != 101 {
		t.Fatalf("ephemeral parameters = %#v", request.EphemeralMessageParameters)
	}
	if request.MessageThreadId != nil {
		t.Fatalf("message thread id = %v, want nil", *request.MessageThreadId)
	}
}

func TestTechnicalProgressGroupTopicUsesEphemeralRichMessage(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "")
	message := deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichMarkdown,
		Text:          "**working**",
		PlainFallback: "working",
	}

	err := adapter.SendProgressMessage(context.Background(), NewLocator(-1001, 77), technicalProgress("tg-101"), &message)
	if err != nil {
		t.Fatalf("SendProgressMessage() error = %v", err)
	}
	if len(tgClient.richMessages) != 1 {
		t.Fatalf("rich messages = %d, want 1", len(tgClient.richMessages))
	}
	request := tgClient.richMessages[0]
	if request.EphemeralMessageParameters == nil || request.EphemeralMessageParameters.ReceiverUserId != 101 {
		t.Fatalf("ephemeral parameters = %#v", request.EphemeralMessageParameters)
	}
	if request.MessageThreadId == nil || *request.MessageThreadId != 77 {
		t.Fatalf("message thread id = %v, want 77", request.MessageThreadId)
	}
}

func TestTechnicalProgressInvalidAudienceFailsWithoutSend(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "")

	err := adapter.SendProgressMessage(context.Background(), NewLocator(-1001, 0), technicalProgress(""), nil)
	if err == nil {
		t.Fatal("SendProgressMessage() error = nil, want invalid audience")
	}
	if len(tgClient.messages) != 0 || len(tgClient.richMessages) != 0 {
		t.Fatalf("provider sends = %d plain, %d rich; want none", len(tgClient.messages), len(tgClient.richMessages))
	}
}

func TestTechnicalProgressInvalidLocatorFailsWithoutSend(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "")

	err := adapter.SendProgressMessage(context.Background(), NewLocator(0, 0), technicalProgress("tg-101"), nil)
	if err == nil {
		t.Fatal("SendProgressMessage() error = nil, want invalid locator")
	}
	if len(tgClient.messages) != 0 || len(tgClient.richMessages) != 0 {
		t.Fatalf("provider sends = %d plain, %d rich; want none", len(tgClient.messages), len(tgClient.richMessages))
	}
}

func TestTechnicalProgressEphemeralFailureHasNoPublicFallback(t *testing.T) {
	tgClient := &fakeTelegramClient{sendErr: errors.New("provider rejected")}
	adapter := newTestAdapter(tgClient, "")
	message := deliveryfmt.Message{
		Name:          deliveryfmt.NameTelegramRichMarkdown,
		Text:          "**working**",
		PlainFallback: "working",
	}

	err := adapter.SendProgressMessage(context.Background(), NewLocator(-1001, 0), technicalProgress("tg-101"), &message)
	if err == nil {
		t.Fatal("SendProgressMessage() error = nil, want provider failure")
	}
	if len(tgClient.richMessages) != 1 || len(tgClient.messages) != 0 {
		t.Fatalf("provider sends = %d rich, %d plain; want one ephemeral attempt", len(tgClient.richMessages), len(tgClient.messages))
	}
}

func technicalProgress(userID string) deliverycmd.Progress {
	return deliverycmd.Progress{
		Kind:     deliverycmd.ProgressTechnical,
		Text:     "working",
		Audience: deliverycmd.ProgressAudience{UserID: userID},
		Visible:  true,
	}
}
