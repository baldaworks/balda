package handlers

import (
	"context"
	"strings"
	"testing"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestGoalRunnerRunGoalLoopUsesExistingAgentSession(t *testing.T) {
	adkRunner, agentSessionID := newBaldaRunTurnTestRunnerWithEvents(t, func(invocationID string) []*adksession.Event {
		done := adksession.NewEvent(invocationID)
		done.Content = genai.NewContentFromText("STATUS: done\nMESSAGE: completed", genai.RoleModel)
		done.TurnComplete = true
		return []*adksession.Event{done}
	})

	locator := baldatelegram.NewLocator(-1002667079342, 8939)
	ts := newSchedulerTopicSession(t, locator, "tg-101", agentSessionID, adkRunner)
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())

	runner := &GoalRunner{
		channel: baldatelegram.NewAdapter(baldatelegram.AdapterParams{
			Messenger: msg,
			TGClient:  tgClient,
			Logger:    zerolog.Nop(),
		}),
		logger:        zerolog.Nop(),
		maxIterations: 1,
	}

	runner.runGoalLoop(context.Background(), locator, ts, "deploy release")

	got := goalRunnerSentText(tgClient)
	if strings.Contains(got, "Goal run failed") {
		t.Fatalf("goal runner sent failure message:\n%s", got)
	}
	if !strings.Contains(got, "Goal iteration 1/1: completed") {
		t.Fatalf("goal runner messages = %q, want completed iteration", got)
	}
	if !strings.Contains(got, "Goal run completed.") {
		t.Fatalf("goal runner messages = %q, want completion message", got)
	}
}

func goalRunnerSentText(tgClient *fakeTelegramClient) string {
	parts := make([]string, 0, len(tgClient.messages))
	for _, msg := range tgClient.messages {
		parts = append(parts, msg.Text)
	}
	return strings.Join(parts, "\n")
}
