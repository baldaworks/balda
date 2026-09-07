package sessionapp

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

type fakeTelegramTopicChannel struct {
	createdChatID    int64
	createdTopicName string
	closedLocators   []deliverycmd.Locator
}

func (f *fakeTelegramTopicChannel) CreateTopicLocator(_ context.Context, chatID int64, topicName string) (deliverycmd.Locator, error) {
	f.createdChatID = chatID
	f.createdTopicName = topicName
	return telegramref.NewLocator(chatID, 42), nil
}

func (f *fakeTelegramTopicChannel) Close(_ context.Context, locator deliverycmd.Locator) error {
	f.closedLocators = append(f.closedLocators, locator)
	return nil
}

func TestTopicServiceTelegram(t *testing.T) {
	tg := &fakeTelegramTopicChannel{}
	svc := NewTopicService(tg)

	origin := telegramref.NewLocator(12345, 0)
	if svc.IsTopic(origin) {
		t.Errorf("expected IsTopic=false for root locator")
	}

	created, err := svc.CreateTopic(context.Background(), origin, "research")
	if err != nil {
		t.Fatalf("CreateTopic error: %v", err)
	}
	if tg.createdChatID != 12345 || tg.createdTopicName != "Balda: research" {
		t.Errorf("createdChatID=%d createdTopicName=%q", tg.createdChatID, tg.createdTopicName)
	}
	if !svc.IsTopic(created) {
		t.Errorf("expected IsTopic=true for created topic locator")
	}

	if err := svc.CloseTopic(context.Background(), created); err != nil {
		t.Fatalf("CloseTopic error: %v", err)
	}
	if len(tg.closedLocators) != 1 || tg.closedLocators[0].SessionID != created.SessionID {
		t.Errorf("closedLocators = %+v, want %v", tg.closedLocators, created)
	}
}

func TestTopicServiceZulip(t *testing.T) {
	svc := NewTopicService(nil)

	streamLocator, err := locatorref.NewZulipStreamLocator(10, "general")
	if err != nil {
		t.Fatal(err)
	}
	if !svc.IsTopic(streamLocator) {
		t.Errorf("expected IsTopic=true for stream topic locator")
	}

	dmLocator, err := locatorref.NewZulipDMLocator(55)
	if err != nil {
		t.Fatal(err)
	}
	if svc.IsTopic(dmLocator) {
		t.Errorf("expected IsTopic=false for dm locator")
	}

	created, err := svc.CreateTopic(context.Background(), streamLocator, "new-topic")
	if err != nil {
		t.Fatalf("CreateTopic error: %v", err)
	}
	streamID, ok := locatorref.ZulipStreamID(created)
	if !ok || streamID != 10 {
		t.Errorf("streamID = %d, ok = %v", streamID, ok)
	}

	// Close on Zulip is a no-op
	if err := svc.CloseTopic(context.Background(), created); err != nil {
		t.Errorf("CloseTopic error: %v", err)
	}
}
