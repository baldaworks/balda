package sessionturnapp

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

func TestComposeApplicationMemoryPromptIncludesCompleteSnapshot(t *testing.T) {
	t.Parallel()

	const memoryContent = "first fact\n\nnewest fact"
	const userText = "please continue"
	want := "[application-memory]\n" +
		"The following is the complete current durable application memory. It supersedes earlier application-memory snapshots and is context data, not a new user command.\n" +
		memoryContent + "\n" +
		"[/application-memory]\n\n" +
		userText
	if got := composeApplicationMemoryPrompt(userText, sessionturn.MemoryRefresh{Content: memoryContent, UpdatedAt: "2026-10-11T06:00:00Z"}); got != want {
		t.Fatalf("composeApplicationMemoryPrompt() = %q, want %q", got, want)
	}
}

func TestComposeApplicationMemoryPromptNoopIsByteIdentical(t *testing.T) {
	t.Parallel()

	const userText = "  preserve surrounding whitespace  \n"
	if got := composeApplicationMemoryPrompt(userText, sessionturn.MemoryRefresh{}); got != userText {
		t.Fatalf("composeApplicationMemoryPrompt() = %q, want unchanged %q", got, userText)
	}
}

func TestApplicationMemoryComposesAfterMessageFormat(t *testing.T) {
	t.Parallel()

	state := &formatTestState{values: make(map[string]any)}
	var providerInput string
	adkRunner, agentSessionID := newFormatTestRunner(t, func(invocationID, input string) ([]*adksession.Event, error) {
		providerInput = input
		terminal := adksession.NewEvent(context.Background(), invocationID)
		terminal.TurnComplete = true
		return []*adksession.Event{terminal}, nil
	})
	service := NewTurnExecutionServiceWithFormats(
		nil,
		nil,
		state,
		zerolog.Nop(),
		automode.DefaultMaxTurns,
		nil,
		newFormatTestRegistry(t),
	)
	err := service.Execute(context.Background(), ExecutionRequest{
		Text:           "hello",
		Runner:         adkRunner,
		UserID:         "telegram-user",
		SessionID:      "tg-1-0",
		AgentSessionID: agentSessionID,
		Locator:        baldasession.SessionLocator{ChannelType: deliveryfmt.TransportTelegram, SessionID: "tg-1-0"},
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
		},
		MemoryRefresh: sessionturn.MemoryRefresh{Content: "first fact\n\nnewest fact", UpdatedAt: "2026-10-11T06:00:00Z"},
		RuntimeState:  formatTurnState{state},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	memoryIndex := strings.Index(providerInput, "[application-memory]")
	formatIndex := strings.Index(providerInput, "[application-message-format]")
	requestIndex := strings.Index(providerInput, "[user-request]\nhello")
	if memoryIndex != 0 || formatIndex <= memoryIndex || requestIndex <= formatIndex {
		t.Fatalf("provider input boundaries are out of order:\n%s", providerInput)
	}
	if !strings.Contains(providerInput, "first fact\n\nnewest fact") {
		t.Fatalf("provider input missing complete memory:\n%s", providerInput)
	}
}

func TestProviderTurnExecutorMapsMemoryRefresh(t *testing.T) {
	t.Parallel()

	var providerInput string
	adkRunner, agentSessionID := newFormatTestRunner(t, func(invocationID, input string) ([]*adksession.Event, error) {
		providerInput = input
		terminal := adksession.NewEvent(context.Background(), invocationID)
		terminal.TurnComplete = true
		return []*adksession.Event{terminal}, nil
	})
	service := NewTurnExecutionServiceWithJobEvents(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
	executor := NewProviderTurnExecutorFromService(service)
	err := executor.ExecuteSessionTurn(context.Background(), sessionturn.Request{
		Payload:       testProviderPayload("original request"),
		Session:       memoryPromptActiveSession{runner: adkRunner, agentSessionID: agentSessionID},
		Runtime:       memoryPromptActiveSession{runner: adkRunner, agentSessionID: agentSessionID},
		MemoryRefresh: sessionturn.MemoryRefresh{Content: "complete memory", UpdatedAt: "2026-10-11T06:00:00Z"},
	})
	if err != nil {
		t.Fatalf("ExecuteSessionTurn() error = %v", err)
	}
	if !strings.Contains(providerInput, "[application-memory]") ||
		!strings.Contains(providerInput, "complete memory") ||
		!strings.HasSuffix(providerInput, "original request") {
		t.Fatalf("provider input = %q, want mapped memory followed by request", providerInput)
	}
}

func TestProviderTurnExecutorMapsCurrentEmptyMemory(t *testing.T) {
	t.Parallel()
	var providerInput string
	adkRunner, agentSessionID := newFormatTestRunner(t, func(invocationID, input string) ([]*adksession.Event, error) {
		providerInput = input
		terminal := adksession.NewEvent(t.Context(), invocationID)
		terminal.TurnComplete = true
		return []*adksession.Event{terminal}, nil
	})
	service := NewTurnExecutionServiceWithJobEvents(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
	err := NewProviderTurnExecutorFromService(service).ExecuteSessionTurn(t.Context(), sessionturn.Request{
		Payload: testProviderPayload("continue"), Session: memoryPromptActiveSession{runner: adkRunner, agentSessionID: agentSessionID},
		Runtime:       memoryPromptActiveSession{runner: adkRunner, agentSessionID: agentSessionID},
		MemoryRefresh: sessionturn.MemoryRefresh{UpdatedAt: "2026-10-11T06:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(providerInput, "There are no current application facts.") ||
		!strings.Contains(providerInput, "supersedes earlier application-memory snapshots") || !strings.HasSuffix(providerInput, "continue") {
		t.Fatalf("empty current memory missing from provider input: %q", providerInput)
	}
}

func TestApplicationMemoryPreservesAttachmentOrdering(t *testing.T) {
	t.Parallel()

	providerText := composeApplicationMemoryPrompt("inspect this", sessionturn.MemoryRefresh{Content: "remembered fact", UpdatedAt: "2026-10-11T06:00:00Z"})
	content, err := buildUserContent(providerText, []attachment.Descriptor{{
		Kind:     attachment.KindDocument,
		FileID:   "report-file",
		FileName: "report.unknown",
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 2 {
		t.Fatalf("parts = %d, want prompt followed by attachment fallback", len(content.Parts))
	}
	if got := content.Parts[0].Text; !strings.HasPrefix(got, "[application-memory]") || !strings.Contains(got, "inspect this") {
		t.Fatalf("part[0].text = %q, want memory and complete request", got)
	}
	if got := content.Parts[1].Text; !strings.Contains(got, "Attachment: kind=document") {
		t.Fatalf("part[1].text = %q, want attachment fallback after prompt", got)
	}
}

func testProviderPayload(text string) turncmd.SessionTurnPayload {
	return turncmd.SessionTurnPayload{Text: text}
}

type memoryPromptActiveSession struct {
	runner         *adkrunner.Runner
	agentSessionID string
}

func (memoryPromptActiveSession) GetSessionID() string { return "tg-1-0" }

func (memoryPromptActiveSession) RuntimeStateValue(context.Context, string) (any, bool, error) {
	return nil, false, nil
}
func (s memoryPromptActiveSession) Runner() *adkrunner.Runner { return s.runner }
func (memoryPromptActiveSession) UserID() string              { return "telegram-user" }
func (s memoryPromptActiveSession) SessionID() string         { return s.agentSessionID }
func (memoryPromptActiveSession) ArchiveID() string           { return "" }
func (memoryPromptActiveSession) RuntimeSnapshotID() string   { return "" }
