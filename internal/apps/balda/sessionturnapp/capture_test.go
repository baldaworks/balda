package sessionturnapp

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type captureHook struct {
	turns []CompletedTurn
	err   error
}

type recordedScheduleOutput struct {
	jobID  string
	output string
}

type recordingScheduleProgress struct{ calls int }

func (p *recordingScheduleProgress) HandleNonTerminal(context.Context, SessionProgressUpdate) (SessionProgressResult, error) {
	p.calls++
	return SessionProgressResult{}, nil
}

type oneShotActiveSession struct {
	runner *runner.Runner
}

func (s *oneShotActiveSession) GetRunner() *runner.Runner  { return s.runner }
func (*oneShotActiveSession) GetRuntimeSnapshotID() string { return "" }
func (*oneShotActiveSession) GetSessionID() string         { return "tg-origin" }
func (*oneShotActiveSession) GetAgentSessionID() string    { return "agent-origin" }
func (*oneShotActiveSession) GetUserID() string            { return "tg-101" }
func (*oneShotActiveSession) RuntimeStateValue(context.Context, string) (any, bool, error) {
	return nil, false, nil
}

func TestOneShotTurnKeepsDistinctReportToLocator(t *testing.T) {
	dispatcher := &captureDispatcher{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil,
		zerolog.Nop(), automode.DefaultMaxTurns, nil)
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		done := adksession.NewEvent(context.Background(), invocationID)
		done.Content = genai.NewContentFromText("one-shot answer", genai.RoleModel)
		done.TurnComplete = true
		return []*adksession.Event{done}
	})
	oneShot := true
	origin := baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "100:0",
		AddressJSON: `{"chat_id":100,"topic_id":0}`, SessionID: "tg-origin"}
	report := baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "200:0",
		AddressJSON: `{"chat_id":200,"topic_id":0}`, SessionID: "tg-report"}
	executor := NewProviderTurnExecutorFromService(service)
	if err := executor.ExecuteSessionTurn(t.Context(), sessionturn.Request{
		Payload: turncmd.SessionTurnPayload{Source: turncmd.SourceSchedule,
			ScheduleOneShot: &oneShot, JobID: "scheduled-once", Text: "input",
			Locator: origin, ReportTo: &report, Deliver: true},
		Session: &oneShotActiveSession{runner: adkRunner}, UserID: "tg-101",
		AgentSessionID: agentSessionID, Runner: adkRunner,
		DeliveryLocator: sessionturn.SessionLocator{ChannelType: report.ChannelType,
			AddressKey: report.AddressKey, AddressJSON: report.AddressJSON, SessionID: report.SessionID},
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("deliveries = %d, want one final report", len(dispatcher.envelopes))
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Locator.AddressKey != report.AddressKey || delivery.Text != "one-shot answer" {
		t.Fatalf("one-shot delivery = %+v", delivery)
	}
}

func (r *recordedScheduleOutput) RecordScheduledOutput(_ context.Context, jobID, output string) error {
	r.jobID, r.output = jobID, output
	return nil
}

func TestPrivateScheduleWithoutLocatorRecordsOutputWithoutDeliveryOrMemoryCapture(t *testing.T) {
	output := &recordedScheduleOutput{}
	capture := &captureHook{}
	dispatcher := &captureDispatcher{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil,
		zerolog.Nop(), automode.DefaultMaxTurns, capture)
	service.SetScheduleOutputRecorder(output)
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		answer := adksession.NewEvent(context.Background(), invocationID)
		answer.Content = genai.NewContentFromText("visible answer", genai.RoleModel)
		done := adksession.NewEvent(context.Background(), invocationID)
		done.Content = genai.NewContentFromText("visible answer", genai.RoleModel)
		done.TurnComplete = true
		return []*adksession.Event{answer, done}
	})
	err := service.Execute(t.Context(), ExecutionRequest{
		Text: "scheduled input", Runner: adkRunner, UserID: "tg-101",
		AgentSessionID: agentSessionID, SessionID: "sch-private-run", JobID: "scheduled-daily-slot",
		Locator: baldasession.SessionLocator{ChannelType: "schedule", AddressKey: "sch-private-run",
			AddressJSON: `{"run_id":"sch-private-run"}`, SessionID: "sch-private-run"},
		TurnSource: "schedule", Deliver: false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v; recorded output = %+v", err, output)
	}
	if output.jobID != "scheduled-daily-slot" || output.output != "visible answer" {
		t.Fatalf("recorded output = %+v", output)
	}
	if len(dispatcher.envelopes) != 0 || len(capture.turns) != 0 {
		t.Fatalf("private schedule leaked delivery or memory: envelopes=%d captures=%d",
			len(dispatcher.envelopes), len(capture.turns))
	}
}

func TestPrivateScheduleProviderFailureKeepsOneBoundedReportAndFailedOutcome(t *testing.T) {
	output := &recordedScheduleOutput{}
	dispatcher := &captureDispatcher{}
	progress := &recordingScheduleProgress{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil,
		zerolog.Nop(), automode.DefaultMaxTurns, &captureHook{})
	service.SetScheduleOutputRecorder(output)
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		thinking := adksession.NewEvent(context.Background(), invocationID)
		thinking.Content = genai.NewContentFromText("intermediate text", genai.RoleModel)
		done := adksession.NewEvent(context.Background(), invocationID)
		done.TurnComplete = true
		done.ErrorCode = "provider_error"
		done.ErrorMessage = "private provider details"
		return []*adksession.Event{thinking, done}
	})
	err := service.Execute(t.Context(), ExecutionRequest{
		Text: "scheduled input", Runner: adkRunner, UserID: "tg-101",
		AgentSessionID: agentSessionID, SessionID: "sch-private-run", JobID: "scheduled-daily-slot",
		Locator: baldasession.SessionLocator{ChannelType: "schedule", AddressKey: "sch-private-run",
			AddressJSON: `{"run_id":"sch-private-run"}`, SessionID: "sch-private-run"},
		DeliveryLocator: baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "123:0",
			AddressJSON: `{"chat_id":123,"topic_id":0}`, SessionID: "tg-123-0"},
		TurnSource: "schedule", Deliver: true, ProgressEmitter: progress,
	})
	if !errors.Is(err, turncmd.ErrScheduledReportQueued) {
		t.Fatalf("Execute() error = %v, want queued failed outcome", err)
	}
	if len(dispatcher.envelopes) != 1 || output.jobID != "scheduled-daily-slot" ||
		output.output == "" || strings.Contains(output.output, "private provider details") {
		t.Fatalf("failure report = %+v, deliveries=%d", output, len(dispatcher.envelopes))
	}
	if progress.calls != 0 {
		t.Fatalf("private schedule sent %d progress updates", progress.calls)
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Locator.ChannelType != "telegram" || delivery.Locator.AddressKey != "123:0" {
		t.Fatalf("final report target = %+v", delivery.Locator)
	}
}

type captureDispatcher struct {
	envelopes []actorlayer.Envelope
}

func (d *captureDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, envelope)
	return &actortransport.DispatchReceipt{}, nil
}

func (h *captureHook) CaptureCompletedTurn(_ context.Context, turn CompletedTurn) error {
	h.turns = append(h.turns, turn)
	return h.err
}

func newCaptureTestRunner(t *testing.T, eventsFn func(string) []*adksession.Event) (*runner.Runner, string) {
	t.Helper()
	ag, err := adkagent.New(adkagent.Config{
		Name:        "session-memory-capture-test",
		Description: "scripted session-memory capture test agent",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				for _, event := range eventsFn(ctx.InvocationID()) {
					if !yield(event, nil) {
						return
					}
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	service := adksession.InMemoryService()
	adkRunner, err := runner.New(runner.Config{
		AppName:        "session-memory-capture-test",
		Agent:          ag,
		SessionService: service,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	session, err := service.Create(context.Background(), &adksession.CreateRequest{
		AppName: "session-memory-capture-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	return adkRunner, session.Session.ID()
}

func TestExecuteCapturesExactlyOneEligibleTurnBeforeDelivery(t *testing.T) {
	t.Parallel()

	hook := &captureHook{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, hook)
	service.now = func() time.Time { return time.Date(2026, 8, 3, 5, 6, 7, 0, time.UTC) }
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		tool := adksession.NewEvent(context.Background(), invocationID)
		tool.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "calendar.lookup", ID: "call-1", Response: map[string]any{"memory_evidence": map[string]any{"schema": trustedToolEvidenceSchema, "text": "2026-08-06"}}}}}}
		answer := adksession.NewEvent(context.Background(), invocationID)
		answer.Content = genai.NewContentFromText("visible answer", genai.RoleModel)
		done := adksession.NewEvent(context.Background(), invocationID)
		done.TurnComplete = true
		return []*adksession.Event{tool, answer, done}
	})
	locator := baldasession.SessionLocator{
		ChannelType: "telegram",
		AddressKey:  "123:0",
		AddressJSON: `{"chat_id":123,"topic_id":0}`,
		SessionID:   "tg-123-0",
	}
	err := service.Execute(context.Background(), ExecutionRequest{
		Text:           "user question",
		Runner:         adkRunner,
		UserID:         "tg-101",
		SessionID:      "balda-session-1",
		AgentSessionID: agentSessionID,
		Locator:        locator,
		Deliver:        false,
		DedupeKey:      "telegram:message:9",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(hook.turns) != 1 {
		t.Fatalf("captured turns = %d, want 1", len(hook.turns))
	}
	turn := hook.turns[0]
	if turn.UserText != "user question" || turn.AssistantText != "visible answer" {
		t.Fatalf("captured turn = %+v", turn)
	}
	if turn.SourceTurnID != "telegram:message:9" || turn.SessionID != "balda-session-1" || turn.AgentSessionID != agentSessionID {
		t.Fatalf("captured identity = %+v", turn)
	}
	if !turn.CompletedAt.Equal(time.Date(2026, 8, 3, 5, 6, 7, 0, time.UTC)) {
		t.Fatalf("captured completion = %s", turn.CompletedAt)
	}
	if len(turn.TrustedTools) != 1 || turn.TrustedTools[0] != (TrustedToolEvidence{Name: "calendar.lookup", CallID: "call-1", Text: "2026-08-06"}) {
		t.Fatalf("captured trusted tools = %#v", turn.TrustedTools)
	}
}

func TestShouldCaptureTerminalTurnIncludesUserOnlyFailures(t *testing.T) {
	failed := adksession.NewEvent(context.Background(), "invocation-failed")
	failed.TurnComplete = true
	failed.ErrorCode = "provider_failure"
	if !shouldCaptureTerminalTurn(failed, "user question", "") {
		t.Fatal("user-only failed terminal turn was not eligible")
	}
	success := adksession.NewEvent(context.Background(), "invocation-success")
	success.TurnComplete = true
	if shouldCaptureTerminalTurn(success, "user question", "") {
		t.Fatal("empty successful terminal turn was eligible")
	}
	if shouldCaptureTerminalTurn(failed, "", "") {
		t.Fatal("failed terminal turn without user text was eligible")
	}
}

func TestExecuteCapturesUserOnlyTerminalProviderFailure(t *testing.T) {
	hook := &captureHook{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, hook)
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		failed := adksession.NewEvent(context.Background(), invocationID)
		failed.TurnComplete = true
		failed.ErrorCode = "provider_failure"
		return []*adksession.Event{failed}
	})
	err := service.Execute(context.Background(), ExecutionRequest{
		Text: "user question", Runner: adkRunner, UserID: "tg-101", SessionID: "balda-session-1", AgentSessionID: agentSessionID,
		Locator: baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "123:0", SessionID: "tg-123-0"}, Deliver: false, DedupeKey: "telegram:message:failed",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(hook.turns) != 1 || hook.turns[0].UserText != "user question" || hook.turns[0].AssistantText != "" || hook.turns[0].TerminalStatus != TerminalStatusFailed {
		t.Fatalf("captured turns = %+v, want one user-only terminal turn", hook.turns)
	}
}

func TestExecuteCaptureFailureDoesNotSuppressResponse(t *testing.T) {
	t.Parallel()

	hook := &captureHook{err: errors.New("puback timeout")}
	dispatcher := &captureDispatcher{}
	service := NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, hook)
	adkRunner, agentSessionID := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		answer := adksession.NewEvent(context.Background(), invocationID)
		answer.Content = genai.NewContentFromText("answer despite memory error", genai.RoleModel)
		done := adksession.NewEvent(context.Background(), invocationID)
		done.TurnComplete = true
		return []*adksession.Event{answer, done}
	})
	if err := service.Execute(context.Background(), ExecutionRequest{
		Text:           "question",
		Runner:         adkRunner,
		UserID:         "tg-101",
		SessionID:      "session-1",
		AgentSessionID: agentSessionID,
		Locator:        baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "123:0", SessionID: "tg-123-0"},
		Deliver:        true,
		DedupeKey:      "turn-1",
	}); err != nil {
		t.Fatalf("Execute() error = %v, want response path to continue", err)
	}
	if len(hook.turns) != 1 {
		t.Fatalf("captured turns = %d, want 1 attempt", len(hook.turns))
	}
	if len(dispatcher.envelopes) == 0 {
		t.Fatalf("delivery envelopes = %d, want a response despite memory failure", len(dispatcher.envelopes))
	}
}

func TestExecuteDoesNotCaptureWithoutTurnCompleteOrVisibleText(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		events func(string) []*adksession.Event
	}{
		{
			name: "no turn complete",
			events: func(invocationID string) []*adksession.Event {
				answer := adksession.NewEvent(context.Background(), invocationID)
				answer.Content = genai.NewContentFromText("partial answer", genai.RoleModel)
				answer.Partial = true
				return []*adksession.Event{answer}
			},
		},
		{
			name: "thought only",
			events: func(invocationID string) []*adksession.Event {
				thought := adksession.NewEvent(context.Background(), invocationID)
				thought.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "hidden", Thought: true}}}
				done := adksession.NewEvent(context.Background(), invocationID)
				done.TurnComplete = true
				return []*adksession.Event{thought, done}
			},
		},
		{
			name: "tool and binary only",
			events: func(invocationID string) []*adksession.Event {
				terminal := adksession.NewEvent(context.Background(), invocationID)
				terminal.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
					{Text: "tool text", FunctionCall: &genai.FunctionCall{Name: "tool"}},
					{Text: "binary text", InlineData: &genai.Blob{MIMEType: "application/octet-stream", Data: []byte("bytes")}},
				}}
				done := adksession.NewEvent(context.Background(), invocationID)
				done.TurnComplete = true
				return []*adksession.Event{terminal, done}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			hook := &captureHook{}
			service := NewTurnExecutionServiceWithJobEventsAndCapture(nil, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns, hook)
			adkRunner, agentSessionID := newCaptureTestRunner(t, test.events)
			err := service.Execute(context.Background(), ExecutionRequest{
				Text:           "question",
				Runner:         adkRunner,
				UserID:         "tg-101",
				SessionID:      "session-1",
				AgentSessionID: agentSessionID,
				Locator:        baldasession.SessionLocator{ChannelType: "telegram", AddressKey: "123:0", SessionID: "tg-123-0"},
				Deliver:        false,
				DedupeKey:      "turn-1",
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if len(hook.turns) != 0 {
				t.Fatalf("captured turns = %d, want 0", len(hook.turns))
			}
		})
	}
}
