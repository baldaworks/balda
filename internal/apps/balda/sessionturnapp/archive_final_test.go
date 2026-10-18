package sessionturnapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/go-adk-acpagent/v2/acperror"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type finalArchiveStub struct {
	messages map[string]chatarchivecmd.Message
	calls    int
	fail     bool
}

func (a *finalArchiveStub) CommitFinal(_ context.Context, m chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error) {
	a.calls++
	if a.fail {
		return m, chatarchivecmd.ErrUnavailable
	}
	for _, key := range members {
		a.messages[key] = m
	}
	return m, nil
}
func (a *finalArchiveStub) FindFinal(_ context.Context, _, key string) (chatarchivecmd.Message, error) {
	m, ok := a.messages[key]
	if !ok {
		return m, chatarchivecmd.ErrNotFound
	}
	return m, nil
}
func TestArchiveFinalCommitFailureBlocksPublication(t *testing.T) {
	calls := 0
	runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
		calls++
		ev := adksession.NewEvent(t.Context(), inv)
		ev.TurnComplete = true
		ev.Content = genai.NewContentFromText("  exact final\n", genai.RoleModel)
		return []*adksession.Event{ev}
	})
	bus := &finalOutcomeDispatcher{}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{fail: true, messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	err := svc.Execute(t.Context(), ExecutionRequest{ArchiveID: "epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session", Text: "input", DedupeKey: "input-id", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}})
	var archival *turncmd.FinalArchiveError
	if !errors.As(err, &archival) || len(bus.envelopes) != 0 || calls != 1 || archive.calls != 3 {
		t.Fatalf("err=%v sends=%d model=%d commits=%d", err, len(bus.envelopes), calls, archive.calls)
	}
}

func TestCommittedFinalReplayBeforeSessionRestore(t *testing.T) {
	calls := 0
	runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
		calls++
		ev := adksession.NewEvent(t.Context(), inv)
		ev.TurnComplete = true
		ev.Content = genai.NewContentFromText("exact final", genai.RoleModel)
		return []*adksession.Event{ev}
	})
	bus := &finalOutcomeDispatcher{err: errors.New("publication unavailable")}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	req := ExecutionRequest{ArchiveID: "old-epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session", Text: "input", DedupeKey: "input-id", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: locator}
	if err := svc.Execute(t.Context(), req); err == nil {
		t.Fatal("expected publication failure")
	}
	if archive.calls != 1 || archive.messages["input-id"].Text != "exact final" {
		t.Fatalf("committed=%+v", archive)
	}
	bus.err = nil
	executor := NewProviderTurnExecutorFromService(svc)
	queued := sessionturn.New(nil, executor, nil, zerolog.Nop())
	payload := turncmd.SessionTurnPayload{ArchiveID: "old-epoch", DedupeKey: "input-id", Deliver: true, Source: turncmd.SourceTelegram, Locator: locator}
	if err := queued.RunSessionTurnPayload(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(bus.envelopes) != 1 || bus.envelopes[0].ID != archive.messages["input-id"].Source {
		t.Fatalf("model=%d delivery=%+v", calls, bus.envelopes)
	}
	// The read/repair API has no delivery side effect.
	if _, err := archive.FindFinal(t.Context(), "old-epoch", "input-id"); err != nil {
		t.Fatal(err)
	}
	if len(bus.envelopes) != 1 {
		t.Fatal("archive read sent a reply")
	}
}

func TestCommittedProviderFailureStaysFailedAfterAcceptance(t *testing.T) {
	bus := &finalOutcomeDispatcher{}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", SessionID: "session"}
	final, err := svc.commitSelectedFinalWithStatus(t.Context(), ExecutionRequest{
		ArchiveID: "epoch", SessionID: "session", DedupeKey: "input-id", Deliver: true, Locator: locator,
	}, "partial\n\nResponse interrupted. The provider is temporarily unavailable.", nil, TerminalStatusFailed)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.resumeFinal(t.Context(), final)
	var accepted *turncmd.FinalOutcomeAcceptedError
	if !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) || len(bus.envelopes) != 1 {
		t.Fatalf("replay error=%v accepted=%t deliveries=%d", err, accepted != nil, len(bus.envelopes))
	}
}

func TestProviderFailureWithoutPartialReplaysCommittedFinalWithoutModelRerun(t *testing.T) {
	calls := 0
	runner, id := newCaptureTestRunner(t, func(invocationID string) []*adksession.Event {
		calls++
		done := adksession.NewEvent(t.Context(), invocationID)
		done.TurnComplete = true
		done.ErrorCode = "acp_request_error:-32603"
		done.ErrorMessage = "credential=SYNTHETIC_SECRET"
		done.CustomMetadata = map[string]any{
			acperror.ProviderErrorMetadataKey: (&acperror.ProviderError{
				Kind: acperror.KindAuthenticationRequired, Scope: acperror.ScopeAccount,
				Message: "credential=SYNTHETIC_SECRET",
			}).Metadata(),
		}
		return []*adksession.Event{done}
	})
	bus := &finalOutcomeDispatcher{err: errors.New("publication unavailable")}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", SessionID: "session"}
	req := ExecutionRequest{
		ArchiveID: "epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session",
		Text: "input", DedupeKey: "input-id", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: locator,
	}
	if err := svc.Execute(t.Context(), req); err == nil {
		t.Fatal("expected publication failure")
	}
	committed := archive.messages["input-id"]
	if archive.calls != 1 || committed.Text == "" || strings.Contains(committed.Text, "SYNTHETIC_SECRET") {
		t.Fatalf("unsafe or missing committed failure: %+v", committed)
	}

	bus.err = nil
	payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "input-id", Deliver: true, Source: turncmd.SourceTelegram, Locator: locator}
	err := sessionturn.New(nil, NewProviderTurnExecutorFromService(svc), nil, zerolog.Nop()).RunSessionTurnPayload(t.Context(), payload)
	var accepted *turncmd.FinalOutcomeAcceptedError
	if !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) {
		t.Fatalf("replay error=%v, want accepted provider failure", err)
	}
	if calls != 1 || len(bus.envelopes) != 1 {
		t.Fatalf("provider calls=%d deliveries=%d, want 1/1", calls, len(bus.envelopes))
	}
}

func TestSteeringReplayPreservesNewConstituents(t *testing.T) {
	bus := &finalOutcomeDispatcher{}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	req := ExecutionRequest{ArchiveID: "epoch", SessionID: "session", DedupeKey: "first", Deliver: true, Locator: locator, SteeringMessages: []turncmd.SteeringMessage{{ArchiveID: "epoch", DedupeKey: "first", Text: "A"}, {ArchiveID: "epoch", DedupeKey: "second", Text: "B"}}}
	final, err := svc.commitSelectedFinal(t.Context(), req, "AB reply", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "first", Deliver: true, Locator: locator, SteeringMessages: append(req.SteeringMessages, turncmd.SteeringMessage{ArchiveID: "epoch", DedupeKey: "third", Text: "new C", MessageID: 3, Attachments: []attachment.Descriptor{{FileName: "new-file"}}})}
	got, handled, err := NewProviderTurnExecutorFromService(svc).ReplaySessionTurn(t.Context(), payload)
	if err != nil || handled || len(got.SteeringMessages) != 1 || got.DedupeKey != "third" || strings.Contains(got.Text, "A") || !strings.Contains(got.Text, "new C") {
		t.Fatalf("remaining=%+v handled=%t error=%v", got, handled, err)
	}
	if got.MessageID != 3 || len(got.Attachments) != 1 || got.Attachments[0].FileName != "new-file" {
		t.Fatalf("remaining metadata=%+v", got)
	}
	if len(bus.envelopes) != 1 || bus.envelopes[0].ID != final.Source {
		t.Fatalf("replayed=%+v", bus.envelopes)
	}
}

type failingFormatState struct {
	formatTestState
	err error
}

func (s *failingFormatState) UpdateRuntimeState(ctx context.Context, locator deliverycmd.Locator, values map[string]any) error {
	if s.err != nil {
		return s.err
	}
	return s.formatTestState.UpdateRuntimeState(ctx, locator, values)
}
func TestFinalSurvivesFormatFailureAndLateReplay(t *testing.T) {
	calls := 0
	runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
		calls++
		ev := adksession.NewEvent(t.Context(), inv)
		ev.TurnComplete = true
		ev.Content = genai.NewContentFromText("final", genai.RoleModel)
		return []*adksession.Event{ev}
	})
	bus := &finalOutcomeDispatcher{}
	state := &failingFormatState{formatTestState: formatTestState{values: map[string]any{chatarchivecmd.EpochStateKey: "epoch"}}, err: errors.New("format unavailable")}
	svc := NewTurnExecutionServiceWithFormats(bus, nil, state, zerolog.Nop(), 1, nil, newFormatTestRegistry(t))
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	req := ExecutionRequest{ArchiveID: "epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session", Text: "input", DedupeKey: "input-id", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: locator, DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}}
	if err := svc.Execute(t.Context(), req); !errors.Is(err, state.err) {
		t.Fatalf("format error=%v", err)
	}
	if archive.calls != 1 || len(bus.envelopes) != 0 {
		t.Fatalf("commit=%d sends=%d", archive.calls, len(bus.envelopes))
	}
	// Reset changes the current runtime. Old replay must keep its old identity and
	// must never write the old format or schedule auto work in the new runtime.
	state.values[chatarchivecmd.EpochStateKey] = "replacement"
	payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "input-id", Deliver: true, Source: turncmd.SourceTelegram, Locator: locator}
	if err := sessionturn.New(nil, NewProviderTurnExecutorFromService(svc), nil, zerolog.Nop()).RunSessionTurnPayload(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || state.updates != 0 || len(bus.envelopes) != 1 || archive.messages["input-id"].SessionID != "epoch" {
		t.Fatalf("calls=%d updates=%d sends=%d", calls, state.updates, len(bus.envelopes))
	}
}

func TestArchiveFinalEligibilityAndStopApproval(t *testing.T) {
	for _, tc := range []struct {
		name, text, source string
		deliver, deny      bool
		want               int
	}{
		{name: "ordinary", text: "visible", source: turncmd.SourceTelegram, deliver: true, want: 1},
		{name: "auto visible", text: "visible auto answer", source: turncmd.SourceAuto, deliver: true, want: 1},
		{name: "auto done", text: automode.DoneSentinel, source: turncmd.SourceAuto, deliver: true},
		{name: "auto waiting", text: automode.WaitSentinel, source: turncmd.SourceAuto, deliver: true},
		{name: "silent", text: "hidden", source: turncmd.SourceTelegram},
		{name: "empty diagnostic", source: turncmd.SourceTelegram, deliver: true},
		{name: "stop rejected", text: "rejected", source: turncmd.SourceTelegram, deliver: true, deny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
				ev := adksession.NewEvent(t.Context(), inv)
				ev.TurnComplete = true
				if tc.text != "" {
					ev.Content = genai.NewContentFromText(tc.text, genai.RoleModel)
				}
				return []*adksession.Event{ev}
			})
			bus := &finalOutcomeDispatcher{}
			svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
			archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
			svc.SetFinalArchive(archive)
			if tc.deny {
				svc.SetHooks(&turnHooks{deny: hookcmd.Stop})
			}
			err := svc.Execute(t.Context(), ExecutionRequest{ArchiveID: "epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session", Text: "synthetic or real prompt", DedupeKey: "input-id", Deliver: tc.deliver, TurnSource: tc.source, Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}})
			if tc.deny {
				if !errors.Is(err, hookcmd.ErrDenied) {
					t.Fatalf("denial=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if archive.calls != tc.want {
				t.Fatalf("commits=%d want=%d", archive.calls, tc.want)
			}
			if tc.want > 0 && strings.Contains(string(archive.messages["input-id"].Replay), "synthetic or real prompt") {
				t.Fatal("replay stored provider prompt")
			}
		})
	}
}

func TestArchivedFinalAcceptedBookkeepingError(t *testing.T) {
	runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
		ev := adksession.NewEvent(t.Context(), inv)
		ev.TurnComplete = true
		ev.Content = genai.NewContentFromText("answer", genai.RoleModel)
		return []*adksession.Event{ev}
	})
	cause := errors.New("bookkeeping failed")
	bus := &finalOutcomeDispatcher{}
	svc := NewTurnExecutionServiceWithJobEvents(bus, finalResultAppender{cause}, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	err := svc.Execute(t.Context(), ExecutionRequest{ArchiveID: "epoch", Runner: runner, AgentSessionID: id, UserID: "tg-101", SessionID: "session", Text: "input", DedupeKey: "input-id", JobID: "job", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}})
	var accepted *turncmd.FinalOutcomeAcceptedError
	if !errors.As(err, &accepted) || !errors.Is(err, cause) || archive.calls != 1 || len(bus.envelopes) != 1 {
		t.Fatalf("error=%v commits=%d sends=%d", err, archive.calls, len(bus.envelopes))
	}
}

func (s *failingFormatState) ActiveRuntimeStateValue(ctx context.Context, locator deliverycmd.Locator, key string) (any, bool, error) {
	return s.RuntimeStateValue(ctx, locator, key)
}
func (s *failingFormatState) UpdateRuntimeStateForArchive(ctx context.Context, locator deliverycmd.Locator, epoch string, values map[string]any) (bool, error) {
	if s.values[chatarchivecmd.EpochStateKey] != epoch {
		return false, nil
	}
	err := s.UpdateRuntimeState(ctx, locator, values)
	return err == nil, err
}

func TestMixedReplayAcceptedFailureKeepsFreshSteering(t *testing.T) {
	bus := &finalOutcomeDispatcher{}
	svc := NewTurnExecutionServiceWithJobEvents(bus, finalResultAppender{errors.New("bookkeeping failed")}, nil, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	members := []turncmd.SteeringMessage{{ArchiveID: "epoch", DedupeKey: "a", Text: "A"}, {ArchiveID: "epoch", DedupeKey: "b", Text: "B"}}
	_, err := svc.commitSelectedFinal(t.Context(), ExecutionRequest{ArchiveID: "epoch", SessionID: "session", JobID: "job", DedupeKey: "a", Locator: locator, SteeringMessages: members}, "AB", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "a", Deliver: true, Locator: locator, SteeringMessages: append(members, turncmd.SteeringMessage{ArchiveID: "epoch", DedupeKey: "c", Text: "fresh C"})}
	got, handled, err := NewProviderTurnExecutorFromService(svc).ReplaySessionTurn(t.Context(), payload)
	if err != nil || handled || len(got.SteeringMessages) != 1 || got.DedupeKey != "c" {
		t.Fatalf("fresh input lost: %+v handled=%t error=%v", got, handled, err)
	}
}

type failedEpochState struct {
	failingFormatState
	readErr error
}

func (s *failedEpochState) ActiveRuntimeStateValue(context.Context, deliverycmd.Locator, string) (any, bool, error) {
	return nil, false, s.readErr
}
func TestCommittedReplayEpochReadFailurePreventsPublication(t *testing.T) {
	bus := &finalOutcomeDispatcher{}
	cause := errors.New("runtime storage unavailable")
	state := &failedEpochState{readErr: cause}
	svc := NewTurnExecutionServiceWithJobEvents(bus, nil, state, zerolog.Nop(), 1)
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	final, err := svc.commitSelectedFinal(t.Context(), ExecutionRequest{ArchiveID: "epoch", DedupeKey: "a", Locator: locator}, "answer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.resumeFinal(t.Context(), final); !errors.Is(err, cause) || len(bus.envelopes) != 0 {
		t.Fatalf("error=%v deliveries=%d", err, len(bus.envelopes))
	}
}

type retryFinalArchive struct {
	finalArchiveStub
	attempts    []chatarchivecmd.Message
	memberships [][]string
}

func (a *retryFinalArchive) CommitFinal(ctx context.Context, message chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error) {
	a.attempts = append(a.attempts, message)
	a.memberships = append(a.memberships, append([]string(nil), members...))
	if len(a.attempts) < 3 {
		return message, chatarchivecmd.ErrUnavailable
	}
	return a.finalArchiveStub.CommitFinal(ctx, message, members)
}
func TestTransientFinalCommitRetriesIdenticalCandidate(t *testing.T) {
	svc := NewTurnExecutionServiceWithJobEvents(nil, nil, nil, zerolog.Nop(), 1)
	archive := &retryFinalArchive{finalArchiveStub: finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	_, err := svc.commitSelectedFinal(t.Context(), ExecutionRequest{ArchiveID: "epoch", DedupeKey: "a", Locator: locator}, "  exact answer\n", nil)
	if err != nil || len(archive.attempts) != 3 {
		t.Fatalf("attempts=%d error=%v", len(archive.attempts), err)
	}
	for i := range archive.attempts {
		if !reflect.DeepEqual(archive.attempts[0], archive.attempts[i]) || !reflect.DeepEqual(archive.memberships[0], archive.memberships[i]) {
			t.Fatal("candidate changed during retry")
		}
	}
}

func (s *failingFormatState) ResumeArchiveRuntime(_ context.Context, _ deliverycmd.Locator, epoch string) (bool, error) {
	return s.values[chatarchivecmd.EpochStateKey] == epoch, nil
}

type restartingFinalState struct {
	failingFormatState
	resumed int
}

func (s *restartingFinalState) ResumeArchiveRuntime(_ context.Context, _ deliverycmd.Locator, epoch string) (bool, error) {
	s.resumed++
	s.values[chatarchivecmd.EpochStateKey] = epoch
	return true, nil
}
func TestCommittedRestartReplayFinishesFormatAndAutoWithoutModel(t *testing.T) {
	bus := &finalOutcomeDispatcher{}
	state := &restartingFinalState{failingFormatState: failingFormatState{formatTestState: formatTestState{values: map[string]any{automode.StateKeyEnabled: true, automode.StateKeyMode: automode.StateRunning}}}}
	svc := NewTurnExecutionServiceWithFormats(bus, nil, state, zerolog.Nop(), 3, nil, newFormatTestRegistry(t))
	archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
	svc.SetFinalArchive(archive)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "101:0", AddressJSON: `{"chat_id":101,"topic_id":0}`, SessionID: "session"}
	_, err := svc.commitSelectedFinal(t.Context(), ExecutionRequest{ArchiveID: "epoch", DedupeKey: "a", Locator: locator, SessionID: "session", UserID: "user", TurnSource: turncmd.SourceTelegram}, "answer", &formatStateChange{announced: true, name: "telegram_rich_markdown"})
	if err != nil {
		t.Fatal(err)
	}
	payload := turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "a", Deliver: true, Source: turncmd.SourceTelegram, Locator: locator}
	if err := sessionturn.New(nil, NewProviderTurnExecutorFromService(svc), nil, zerolog.Nop()).RunSessionTurnPayload(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	if state.resumed != 1 || state.values[stateKeyMessageFormatAnnounced] != true {
		t.Fatalf("restored bookkeeping=%+v", state)
	}
	autoCount := 0
	for _, env := range bus.envelopes {
		if env.To.Target == actorcmd.ActorTypeSession && env.Namespace != actorcmd.NamespaceAutoModeCommand {
			var next turncmd.SessionTurnPayload
			if err := actorlayer.UnmarshalPayload(env.Payload, &next); err != nil {
				t.Fatal(err)
			}
			if next.ArchiveID != "epoch" || next.Source != turncmd.SourceAuto || next.DedupeKey != autoTurnDedupeKey("session", "a", 1) {
				t.Fatalf("auto=%+v", next)
			}
			autoCount++
		}
	}
	if autoCount != 1 {
		t.Fatalf("auto continuations=%d", autoCount)
	}
}
