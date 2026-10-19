package commandfx

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	commandskill "github.com/baldaworks/balda/internal/apps/balda/actors/command/skill"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// This crosses command publication, queued preparation, and the real ADK lookup.
// A requester-derived lookup must fail because only runtime A exists.
func TestPublishedCommandsContinueSharedProviderSession(t *testing.T) {
	for _, caller := range []string{"runtime-a", "requester-b"} {
		t.Run(caller, func(t *testing.T) {
			source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
			old := skillSnapshot(skillMetadata(source, "review", "retained-revision"))
			descriptor := runtimecatalogcmd.CommandDescriptor{
				ID:       runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindCommand, Name: "review-command"},
				Revision: "retained-command", Name: "review-command", Instruction: "retained plugin instruction", Advertised: true,
			}
			old.Commands = map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor{descriptor.ID: descriptor}
			newer := old.Clone()
			newer.ID = "snapshot-new"
			newer.Skills[skillMetadata(source, "review", "current-revision").ID] = skillMetadata(source, "review", "current-revision")
			updated := descriptor
			updated.Instruction = "current plugin instruction"
			newer.Commands[descriptor.ID] = updated
			catalog := &bindingCatalog{snapshots: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{old.ID: old, newer.ID: newer}}
			skills, err := baldaagent.NewSkillManager(catalog, bindingSkillReader{}, baldaagent.SkillMetadataBudget{})
			if err != nil {
				t.Fatal(err)
			}
			locator := deliverycmd.Locator{SessionID: "shared-topic", ChannelType: "telegram", AddressKey: "10:42", AddressJSON: `{"chat_id":10,"topic_id":42}`}
			var inputs []string
			active := newBindingSession(t, locator.SessionID, "runtime-a", func(ctx adkagent.InvocationContext) {
				interaction, ok := permissioncmd.InteractionFromContext(ctx)
				if !ok || interaction.RequestedBy.UserID != caller || interaction.Locator != locator {
					t.Errorf("permission interaction = %+v, present=%t, want requester %s at original locator", interaction, ok, caller)
				}
				inputs = append(inputs, bindingContent(ctx.UserContent()))
			})
			accessor := &bindingAccessor{active: active}
			dispatcher := &skillTurnDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: "accepted"}}
			service := sessionturnapp.NewTurnExecutionServiceWithJobEvents(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)
			queued := sessionturn.NewWithSkillLoader(accessor, sessionturnapp.NewProviderTurnExecutorFromService(service), nil, skills, zerolog.Nop())
			router, err := commandactor.NewRouter([]commandactor.Handler{commandskill.New(NewSkillTurnExecutor(skills, dispatcher), dispatcher, zerolog.Nop())})
			if err != nil {
				t.Fatal(err)
			}
			actor := commandactor.NewActor(router, catalog, NewPluginTurnExecutor(dispatcher))
			originalRunner := active.runner
			for i, name := range []string{"skill", "review-command"} {
				payload := testSkillCommandPayload()
				payload.Principal, payload.Name, payload.Locator = caller, name, locator
				payload.Args = "demo:review use prior context"
				parent, err := commandcmd.NewEnvelope(payload, commandcmd.EnvelopeOptions{ID: fmt.Sprintf("command-%d", i), From: actorlayer.ActorAddress{Target: "telegram", Key: caller}})
				if err != nil {
					t.Fatal(err)
				}
				if err := actor.Handle(t.Context(), parent); err != nil {
					t.Fatal(err)
				}
				suffix := ":skill-turn"
				if name != "skill" {
					suffix = ":plugin-turn"
				}
				child, ok := dispatcher.envelopes[parent.ID+suffix]
				if !ok {
					t.Fatal("command did not publish child turn")
				}
				var turn turncmd.SessionTurnPayload
				if err := actorlayer.UnmarshalPayload(child.Payload, &turn); err != nil {
					t.Fatal(err)
				}
				if turn.RequesterUserID != caller || turn.Locator != locator {
					t.Fatalf("published context = %+v", turn)
				}
				// Restoration must not let the queued caller overwrite the saved binding.
				accessor.restore = i == 1
				if err := queued.RunSessionTurnPayload(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				if len(inputs) != i+1 {
					t.Fatalf("provider invocations = %d, want %d", len(inputs), i+1)
				}
				assertBindingHistory(t, active, i+1)
			}
			if !strings.Contains(inputs[0], "retained skill instructions") || strings.Contains(inputs[0], "current-revision") ||
				!strings.Contains(inputs[1], "retained plugin instruction") || strings.Contains(inputs[1], "current plugin instruction") {
				t.Fatalf("provider inputs did not retain pinned content: %q", inputs)
			}
			// A later ordinary chat must continue the same state and history.
			if err := queued.RunSessionTurnPayload(t.Context(), turncmd.SessionTurnPayload{Text: "continue chat", UserID: "runtime-a", RequesterUserID: caller, Locator: locator}); err != nil {
				t.Fatal(err)
			}
			assertBindingHistory(t, active, 3)
			if active.runner != originalRunner || accessor.restores != 1 {
				t.Fatal("session runtime or restore selection changed")
			}
			// Invalid and unavailable pins reject before invocation, without replacing A.
			for _, selection := range []runtimecatalogcmd.SkillSelection{
				{Snapshot: old.ID, Ref: runtimecatalogcmd.SkillRef{Source: source, Name: "review", Revision: "wrong-revision"}},
				{Snapshot: "unavailable", Ref: runtimecatalogcmd.SkillRef{Source: source, Name: "review", Revision: "retained-revision"}},
			} {
				err := queued.RunSessionTurnPayload(t.Context(), turncmd.SessionTurnPayload{Text: "invalid", UserID: caller, RequesterUserID: caller, Locator: locator, Skill: &selection})
				if !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) || len(inputs) != 3 {
					t.Fatalf("invalid pin: err=%v invocations=%d", err, len(inputs))
				}
			}
			assertBindingHistory(t, active, 3)
		})
	}
}

type bindingCatalog struct {
	snapshots map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
}

func (c *bindingCatalog) RetainedSkillSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	s, ok := c.snapshots[id]
	if !ok {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	return s.Clone(), nil
}
func (c *bindingCatalog) ResolveCommandSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	return c.RetainedSkillSnapshot(ctx, id)
}

type bindingSkillReader struct{}

func (bindingSkillReader) ReadSkill(_ context.Context, req runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	if req.Ref.Revision != "retained-revision" {
		return runtimecatalogcmd.LoadedSkill{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return runtimecatalogcmd.LoadedSkill{Ref: req.Ref, Instructions: "retained skill instructions"}, nil
}

type bindingSession struct {
	runner               *adkrunner.Runner
	service              adksession.Service
	id, user, providerID string
	calls                int
}

func newBindingSession(t *testing.T, id, user string, observe func(adkagent.InvocationContext)) *bindingSession {
	t.Helper()
	s := &bindingSession{service: adksession.InMemoryService(), id: id, user: user, providerID: id + "-memory-lifetime"}
	created, err := s.service.Create(t.Context(), &adksession.CreateRequest{AppName: "binding-test", UserID: user, SessionID: s.providerID})
	if err != nil {
		t.Fatal(err)
	}
	prior := adksession.NewEvent(t.Context(), "seed")
	prior.Author = "user"
	prior.Content = genai.NewContentFromText("recognizable earlier conversation", genai.RoleUser)
	prior.Actions.StateDelta = map[string]any{"retained-state": "recognizable state"}
	if err := s.service.AppendEvent(t.Context(), created.Session, prior); err != nil {
		t.Fatal(err)
	}
	agent, err := adkagent.New(adkagent.Config{Name: "binding-test", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			s.calls++
			if value, err := ctx.Session().State().Get("retained-state"); err != nil || value != "recognizable state" {
				t.Errorf("provider lost seeded state: %v, %v", value, err)
			}
			if observe != nil {
				observe(ctx)
			}
			event := adksession.NewEvent(ctx, ctx.InvocationID())
			event.Content = genai.NewContentFromText("completed", genai.RoleModel)
			event.TurnComplete = true
			yield(event, nil)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	s.runner, err = adkrunner.New(adkrunner.Config{AppName: "binding-test", Agent: agent, SessionService: s.service})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (s *bindingSession) GetRunner() *adkrunner.Runner { return s.runner }
func (*bindingSession) GetRuntimeSnapshotID() string   { return "snapshot-one" }
func (s *bindingSession) GetSessionID() string         { return s.id }
func (s *bindingSession) GetAgentSessionID() string    { return s.providerID }
func (s *bindingSession) GetUserID() string            { return s.user }
func (s *bindingSession) RuntimeStateValue(ctx context.Context, key string) (any, bool, error) {
	response, err := s.service.Get(ctx, &adksession.GetRequest{AppName: "binding-test", UserID: s.user, SessionID: s.providerID})
	if err != nil {
		return nil, false, err
	}
	value, err := response.Session.State().Get(key)
	if errors.Is(err, adksession.ErrStateKeyNotExist) {
		return nil, false, nil
	}
	return value, err == nil, err
}
func assertBindingHistory(t *testing.T, s *bindingSession, turns int) {
	t.Helper()
	response, err := s.service.Get(t.Context(), &adksession.GetRequest{AppName: "binding-test", UserID: s.user, SessionID: s.providerID})
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != turns || response.Session.Events().Len() != 1+turns*2 {
		t.Fatalf("provider calls=%d history=%d, want %d and %d", s.calls, response.Session.Events().Len(), turns, 1+turns*2)
	}
	first := response.Session.Events().At(0)
	if bindingContent(first.Content) != "recognizable earlier conversation" {
		t.Fatal("prior conversation changed")
	}
	if value, err := response.Session.State().Get("retained-state"); err != nil || value != "recognizable state" {
		t.Fatalf("retained state=%v err=%v", value, err)
	}
}
func bindingContent(content *genai.Content) string {
	var text strings.Builder
	if content != nil {
		for _, part := range content.Parts {
			if part != nil {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

type bindingAccessor struct {
	active    *bindingSession
	restore   bool
	restores  int
	transient bool
}

func (a *bindingAccessor) GetSession(locator sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	if locator.SessionID != a.active.id {
		return nil, errors.New("unexpected recipient lookup")
	}
	if a.restore || a.transient {
		return nil, errors.New("inactive")
	}
	return a.active, nil
}
func (a *bindingAccessor) RestoreSession(context.Context, sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	if !a.restore {
		return nil, errors.New("unexpected restore")
	}
	a.restore = false
	a.restores++
	return a.active, nil
}
func (*bindingAccessor) EnsureSession(context.Context, sessionturn.SessionContext, string) (sessionturn.ActiveSession, error) {
	return nil, errors.New("unexpected creation")
}
func (a *bindingAccessor) EnsureTransientSession(_ context.Context, ctx sessionturn.SessionContext, _ string) (sessionturn.ActiveSession, error) {
	if !a.transient || ctx.Locator.SessionID != a.active.id || ctx.UserID != a.active.user {
		return nil, errors.New("unexpected transient creation")
	}
	a.transient = false
	return a.active, nil
}

func TestPrivateTurnsKeepProviderBindingSeparateFromReportConversation(t *testing.T) {
	for _, source := range []string{turncmd.SourceSchedule, turncmd.SourceWebhook} {
		for _, transient := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/transient=%t", source, transient), func(t *testing.T) {
				report := deliverycmd.Locator{SessionID: "tg-20-42", ChannelType: "telegram", AddressKey: "20:42", AddressJSON: `{"chat_id":20,"topic_id":42}`}
				recipient := newBindingSession(t, report.SessionID, "recipient-user", nil)
				privateID := "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				if source == turncmd.SourceSchedule {
					privateID = turncmd.ScheduledExecutionSessionID("scheduled-review-slot")
				}
				private := newBindingSession(t, privateID, "private-user", nil)
				locator := deliverycmd.Locator{SessionID: privateID, ChannelType: source, AddressKey: privateID, AddressJSON: "{}"}
				accessor := &bindingAccessor{active: private, transient: transient}
				executor := &bindingProviderExecutor{}
				payload := turncmd.SessionTurnPayload{JobID: "scheduled-review-slot", ScheduledJobID: "review", Source: source,
					UserID: "private-user", AgentSessionID: "stale-recipient-provider-id", Locator: locator, ReportTo: &report, Deliver: true, Text: "private review"}
				if source == turncmd.SourceSchedule {
					payload.Locator = report
				}
				if !transient {
					payload.UserID = "stale-recipient-user"
				}
				if err := sessionturn.New(accessor, executor, nil, zerolog.Nop()).RunSessionTurnPayload(t.Context(), payload); err != nil {
					t.Fatal(err)
				}
				if executor.request.Payload.Locator.SessionID != privateID || executor.request.DeliveryLocator.SessionID != report.SessionID || executor.request.DeliveryLocator.AddressKey != report.AddressKey || !executor.request.Payload.Deliver {
					t.Fatalf("private execution/report routing = %+v", executor.request)
				}
				if accessor.restores != 0 || accessor.transient {
					t.Fatal("private run changed lifecycle selection")
				}
				assertBindingHistory(t, private, 1)
				assertBindingHistory(t, recipient, 0)
			})
		}
	}
}

type bindingProviderExecutor struct{ request sessionturn.Request }

func (e *bindingProviderExecutor) ExecuteSessionTurn(ctx context.Context, request sessionturn.Request) error {
	e.request = request
	for _, err := range request.Runner.Run(ctx, request.UserID, request.AgentSessionID, genai.NewContentFromText(request.Payload.Text, genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			return err
		}
	}
	return nil
}
