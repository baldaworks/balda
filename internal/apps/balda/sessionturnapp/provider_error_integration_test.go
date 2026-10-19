package sessionturnapp

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	mattermostpresentation "github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/presentation"
	slackpresentation "github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/presentation"
	telegrampresentation "github.com/baldaworks/balda/internal/apps/balda/channel/telegram/presentation"
	zulippresentation "github.com/baldaworks/balda/internal/apps/balda/channel/zulip/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/runtime/v2/poolagent"
	"github.com/normahq/go-adk-acpagent/v2/acperror"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Keep the protocol fixture independent of the production classification constant.
const fixtureOverloadCode = "serverOverloaded"

type poolFixtureCreator map[string]adkagent.Agent

func (c poolFixtureCreator) CreateAgent(_ context.Context, name string, _ poolagent.AgentRequest) (adkagent.Agent, error) {
	return c[name], nil
}

func TestProviderPoolFallbackAndToolOutputBoundary(t *testing.T) {
	for _, withTool := range []bool{false, true} {
		name := "fallback succeeds"
		if withTool {
			name = "tool output makes failure terminal"
		}
		t.Run(name, func(t *testing.T) {
			primaryRuns, fallbackRuns := 0, 0
			primary, err := adkagent.New(adkagent.Config{Name: "primary", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) {
					primaryRuns++
					if withTool {
						tool := adksession.NewEvent(ctx, ctx.InvocationID())
						tool.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "lookup", Response: map[string]any{"result": "tool-body-not-a-reply"}}}}}
						if !yield(tool, nil) {
							return
						}
					}
					failure := adksession.NewEvent(ctx, ctx.InvocationID())
					failure.TurnComplete = true
					failure.ErrorCode = fixtureOverloadCode
					failure.ErrorMessage = "primary model is busy"
					failure.CustomMetadata = map[string]any{acperror.ProviderErrorMetadataKey: (&acperror.ProviderError{Kind: acperror.KindUnavailable, Scope: acperror.ScopeProvider, Code: fixtureOverloadCode, Message: "primary model is busy"}).Metadata()}
					yield(failure, nil)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			fallback, err := adkagent.New(adkagent.Config{Name: "fallback", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) {
					fallbackRuns++
					done := adksession.NewEvent(ctx, ctx.InvocationID())
					done.TurnComplete = true
					done.Content = genai.NewContentFromText("**successful fallback**", genai.RoleModel)
					yield(done, nil)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := poolagent.NewPoolAgent(t.Context(), "providers", []poolagent.MemberConfig{{Name: "primary"}, {Name: "fallback"}}, poolagent.AgentRequest{}, poolFixtureCreator{"primary": primary, "fallback": fallback})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := pool.Close(); err != nil {
					t.Errorf("close pool: %v", err)
				}
			})
			runner, id := newCaptureTestRunnerForAgent(t, pool)
			bus := &captureDispatcher{}
			capture := &captureHook{}
			svc := NewTurnExecutionServiceWithJobEventsAndCapture(bus, nil, nil, zerolog.Nop(), 1, capture)
			registry := &testTurnFailureRegistry{}
			svc.structuredRegistry = registry
			err = svc.Execute(t.Context(), ExecutionRequest{Text: "question", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "session", DedupeKey: "pool-turn", Deliver: true, TurnSource: turncmd.SourceTelegram, ProgressEmitter: &recordingScheduleProgress{}, DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}, Locator: deliverycmd.Locator{SessionID: "session", ChannelType: "telegram", AddressKey: "101:0"}})
			if primaryRuns != 1 {
				t.Fatalf("primary runs=%d", primaryRuns)
			}
			if len(bus.envelopes) != 1 || len(capture.turns) != 1 {
				t.Fatalf("deliveries=%d captures=%d", len(bus.envelopes), len(capture.turns))
			}
			var final deliverycmd.Payload
			if err := actorlayer.UnmarshalPayload(bus.envelopes[0].Payload, &final); err != nil {
				t.Fatal(err)
			}
			if withTool {
				var accepted *turncmd.FinalOutcomeAcceptedError
				if !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) || fallbackRuns != 0 || registry.calls != 1 || capture.turns[0].TerminalStatus != TerminalStatusFailed {
					t.Fatalf("error=%v fallback=%d renders=%d status=%s", err, fallbackRuns, registry.calls, capture.turns[0].TerminalStatus)
				}
				if !strings.Contains(final.Text, "Response interrupted.") || !strings.Contains(final.Text, "primary model is busy") || !strings.Contains(final.Text, "choose another model") || strings.Contains(final.Text, "tool-body-not-a-reply") {
					t.Fatalf("terminal text=%q", final.Text)
				}
			} else {
				if err != nil || fallbackRuns != 1 || registry.calls != 0 || capture.turns[0].TerminalStatus != TerminalStatusSuccess {
					t.Fatalf("error=%v fallback=%d renders=%d status=%s", err, fallbackRuns, registry.calls, capture.turns[0].TerminalStatus)
				}
				if final.Text != "**successful fallback**" || final.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
					t.Fatalf("successful final=%+v", final)
				}
			}
		})
	}
}

// realFailureRenderer bridges the registry interface to the actual transport-owned
// rendering functions; production registration/format routes are tested by each Fx package.
type realFailureRenderer struct {
	render func(deliveryfmt.TurnFailure) string
	format deliveryfmt.DeliveryFormat
}

func (r realFailureRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: r.render(env.Body), DeliveryFormat: r.format}, nil
}

func TestProviderFailureUsesRealLiteralRendererAndFrozenArchiveReplay(t *testing.T) {
	const original = "  Модель & <busy> &amp; **retry** [link](https://example.com)\n```\n````\nnext line  "
	for _, tt := range []struct {
		transport string
		render    func(deliveryfmt.TurnFailure) string
		format    deliveryfmt.DeliveryFormat
	}{
		{deliveryfmt.TransportTelegram, telegrampresentation.RenderTurnFailure, deliveryfmt.DeliveryFormatNone},
		{deliveryfmt.TransportSlackAgent, slackpresentation.RenderTurnFailure, deliveryfmt.DeliveryFormatNone},
		{deliveryfmt.TransportZulip, zulippresentation.RenderTurnFailure, deliveryfmt.DeliveryFormatNone},
		{deliveryfmt.TransportMattermost, mattermostpresentation.RenderTurnFailure, deliveryfmt.DeliveryFormatNone},
	} {
		t.Run(tt.transport, func(t *testing.T) {
			providerRuns := 0
			runner, id := newCaptureTestRunner(t, func(inv string) []*adksession.Event {
				providerRuns++
				done := adksession.NewEvent(t.Context(), inv)
				done.TurnComplete = true
				done.ErrorCode = fixtureOverloadCode
				done.ErrorMessage = original
				return []*adksession.Event{done}
			})
			bus := &finalOutcomeDispatcher{err: errors.New("publication unavailable")}
			archive := &finalArchiveStub{messages: map[string]chatarchivecmd.Message{}}
			svc := NewTurnExecutionServiceWithJobEvents(bus, nil, nil, zerolog.Nop(), 1)
			registry := deliveryfmt.NewStructuredRegistry()
			if err := deliveryfmt.RegisterStructuredRenderer(registry, tt.transport, deliveryfmt.TurnFailureDescriptor, realFailureRenderer{render: tt.render, format: tt.format}); err != nil {
				t.Fatal(err)
			}
			svc.structuredRegistry = registry
			svc.SetFinalArchive(archive)
			locator := deliverycmd.Locator{SessionID: "session", ChannelType: tt.transport, AddressKey: "synthetic-target"}
			req := ExecutionRequest{ArchiveID: "epoch", DedupeKey: "turn", Text: "input", Runner: runner, UserID: "tg-101", AgentSessionID: id, SessionID: "session", Deliver: true, TurnSource: turncmd.SourceTelegram, Locator: locator, DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}}
			if err := svc.Execute(t.Context(), req); err == nil {
				t.Fatal("expected publication failure")
			}
			committed := archive.messages["turn"]
			semantic := "Provider error:\n" + original + "\n\nThe selected model is overloaded. Try again later or choose another model."
			if committed.Text != semantic {
				t.Fatalf("archive text=%q, want original semantic text %q", committed.Text, semantic)
			}
			var replay finalReplay
			if err := json.Unmarshal(committed.Replay, &replay); err != nil {
				t.Fatal(err)
			}
			if replay.TerminalStatus != TerminalStatusFailed {
				t.Fatalf("frozen status=%s", replay.TerminalStatus)
			}
			var frozen deliverycmd.Payload
			if err := actorlayer.UnmarshalPayload(replay.Envelope.Payload, &frozen); err != nil {
				t.Fatal(err)
			}
			display := frozen.Text
			switch tt.transport {
			case deliveryfmt.TransportSlackAgent:
				display = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(display)
			case deliveryfmt.TransportZulip, deliveryfmt.TransportMattermost:
				fence, rest, ok := strings.Cut(display, "\n")
				if !ok {
					t.Fatal("missing opening fence")
				}
				display, ok = strings.CutSuffix(rest, "\n"+fence)
				if !ok {
					t.Fatal("missing closing fence")
				}
			}
			if display != semantic || frozen.DeliveryFormat != tt.format {
				t.Fatalf("display=%q format=%s", display, frozen.DeliveryFormat)
			}
			svc.structuredRegistry = &testTurnFailureRegistry{err: errors.New("replay cannot render")}
			bus.err = nil
			_, handled, err := NewProviderTurnExecutorFromService(svc).ReplaySessionTurn(t.Context(), turncmd.SessionTurnPayload{ArchiveID: "epoch", DedupeKey: "turn", Deliver: true, Source: turncmd.SourceTelegram, Locator: locator})
			var accepted *turncmd.FinalOutcomeAcceptedError
			if !handled || !errors.Is(err, errProviderTurnFailed) || !errors.As(err, &accepted) || providerRuns != 1 || archive.calls != 1 || len(bus.envelopes) != 1 {
				t.Fatalf("handled=%t error=%v provider=%d commits=%d sends=%d", handled, err, providerRuns, archive.calls, len(bus.envelopes))
			}
			if !reflect.DeepEqual(bus.envelopes[0].Payload, replay.Envelope.Payload) {
				t.Fatal("replay changed frozen delivery representation")
			}
		})
	}
}
