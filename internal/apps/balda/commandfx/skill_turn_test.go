package commandfx

import (
	"context"
	"errors"
	"testing"

	commandskill "github.com/baldaworks/balda/internal/apps/balda/actors/command/skill"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type retainedSkillCatalog struct {
	snapshot runtimecatalogcmd.Snapshot
	err      error
}

func (c *retainedSkillCatalog) RetainedSkillSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	if c.err != nil {
		return runtimecatalogcmd.Snapshot{}, c.err
	}
	if c.snapshot.ID != id {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	return c.snapshot.Clone(), nil
}

type unreadSkillContent struct {
	reads int
}

func (r *unreadSkillContent) ReadSkill(context.Context, runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	r.reads++
	return runtimecatalogcmd.LoadedSkill{}, errors.New("skill body must remain lazy")
}

type skillTurnDispatcher struct {
	receipt   *actortransport.DispatchReceipt
	err       error
	envelopes map[string]actorlayer.Envelope
}

func (d *skillTurnDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if d.err != nil {
		return nil, d.err
	}
	if d.envelopes == nil {
		d.envelopes = make(map[string]actorlayer.Envelope)
	}
	if _, exists := d.envelopes[envelope.DedupeKey]; !exists {
		d.envelopes[envelope.DedupeKey] = envelope
	}
	return d.receipt, nil
}

func TestSkillTurnExecutorPublishesPinnedDeduplicatedTurn(t *testing.T) {
	pluginSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"}
	userSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "default"}
	snapshot := skillSnapshot(
		skillMetadata(pluginSource, "story", "plugin-revision"),
		skillMetadata(userSource, "story", "user-revision"),
	)
	reader := &unreadSkillContent{}
	manager := newTestSkillManager(t, snapshot, reader)
	dispatcher := &skillTurnDispatcher{
		receipt:   &actortransport.DispatchReceipt{MsgID: "turn"},
		envelopes: make(map[string]actorlayer.Envelope),
	}
	executor := NewSkillTurnExecutor(manager, dispatcher)
	payload := testSkillCommandPayload()
	parent := actorlayer.Envelope{ID: "command-id", DedupeKey: "command-dedupe", CorrelationID: "correlation"}
	selector := commandskill.Selector{Plugin: "prism", Name: "story"}

	for range 2 {
		if err := executor.ExecuteSkill(context.Background(), parent, payload, selector, " implement this feature "); err != nil {
			t.Fatalf("ExecuteSkill() error = %v", err)
		}
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("durable child turns after redelivery = %d, want 1", len(dispatcher.envelopes))
	}
	if reader.reads != 0 {
		t.Fatalf("skill body reads = %d, want 0", reader.reads)
	}

	envelope := dispatcher.envelopes["command-dedupe:skill-turn"]
	if envelope.DedupeKey != "command-dedupe:skill-turn" || envelope.CorrelationID != "correlation" || envelope.CausationID != "command-id" {
		t.Fatalf("child identity = %+v", envelope)
	}
	var turn turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(envelope.Payload, &turn); err != nil {
		t.Fatalf("UnmarshalPayload() error = %v", err)
	}
	wantSelection := runtimecatalogcmd.SkillSelection{
		Snapshot: snapshot.ID,
		Ref:      runtimecatalogcmd.SkillRef{Source: pluginSource, Revision: "plugin-revision", Name: "story"},
	}
	if turn.Skill == nil || *turn.Skill != wantSelection {
		t.Fatalf("skill selection = %+v, want %+v", turn.Skill, wantSelection)
	}
	if turn.Text != "implement this feature" || turn.Locator != payload.Locator || turn.UserID != payload.Principal || turn.RequesterUserID != payload.Principal {
		t.Fatalf("turn request context = %+v", turn)
	}
	if turn.DeliveryFormat != payload.Presentation.DeliveryFormat || turn.ProgressPolicy != payload.Presentation.ProgressPolicy || turn.Source != payload.Transport || !turn.Deliver {
		t.Fatalf("turn delivery context = %+v", turn)
	}
}

func TestSkillTurnExecutorResolvesUniqueUnqualifiedSkillWithEmptyPrompt(t *testing.T) {
	workspaceSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindWorkspaceSkill, Name: "workspace"}
	snapshot := skillSnapshot(skillMetadata(workspaceSource, "review", "revision-one"))
	dispatcher := &skillTurnDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: "turn"}}
	executor := NewSkillTurnExecutor(newTestSkillManager(t, snapshot, &unreadSkillContent{}), dispatcher)

	if err := executor.ExecuteSkill(
		context.Background(),
		actorlayer.Envelope{ID: "command-id"},
		testSkillCommandPayload(),
		commandskill.Selector{Name: "review"},
		"",
	); err != nil {
		t.Fatalf("ExecuteSkill() error = %v", err)
	}
	var turn turncmd.SessionTurnPayload
	for _, envelope := range dispatcher.envelopes {
		if err := actorlayer.UnmarshalPayload(envelope.Payload, &turn); err != nil {
			t.Fatalf("UnmarshalPayload() error = %v", err)
		}
	}
	if turn.Text != "" || turn.Skill == nil || turn.Skill.Ref.Source != workspaceSource || turn.Skill.Ref.Name != "review" {
		t.Fatalf("turn = %+v", turn)
	}
}

func TestSkillTurnExecutorMapsSelectionFailures(t *testing.T) {
	firstSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "one"}
	secondSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindWorkspaceSkill, Name: "two"}
	tests := []struct {
		name     string
		snapshot runtimecatalogcmd.Snapshot
		selector commandskill.Selector
		wantErr  error
	}{
		{
			name:     "not found",
			snapshot: skillSnapshot(),
			selector: commandskill.Selector{Name: "missing"},
			wantErr:  commandskill.ErrNotFound,
		},
		{
			name: "ambiguous unqualified name",
			snapshot: skillSnapshot(
				skillMetadata(firstSource, "review", "revision-one"),
				skillMetadata(secondSource, "review", "revision-two"),
			),
			selector: commandskill.Selector{Name: "review"},
			wantErr:  commandskill.ErrAmbiguous,
		},
		{
			name:     "plugin qualification does not fall back",
			snapshot: skillSnapshot(skillMetadata(firstSource, "story", "revision-one")),
			selector: commandskill.Selector{Plugin: "prism", Name: "story"},
			wantErr:  commandskill.ErrNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &skillTurnDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: "turn"}}
			executor := NewSkillTurnExecutor(newTestSkillManager(t, test.snapshot, &unreadSkillContent{}), dispatcher)
			err := executor.ExecuteSkill(context.Background(), actorlayer.Envelope{ID: "command-id"}, testSkillCommandPayload(), test.selector, "prompt")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ExecuteSkill() error = %v, want %v", err, test.wantErr)
			}
			if len(dispatcher.envelopes) != 0 {
				t.Errorf("child turns = %d, want 0", len(dispatcher.envelopes))
			}
		})
	}
}

func TestSkillTurnExecutorMapsUnavailableSnapshot(t *testing.T) {
	manager, err := baldaagent.NewSkillManager(
		&retainedSkillCatalog{err: runtimecatalogcmd.ErrSnapshotUnavailable},
		&unreadSkillContent{},
		baldaagent.SkillMetadataBudget{},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &skillTurnDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: "turn"}}
	executor := NewSkillTurnExecutor(manager, dispatcher)

	err = executor.ExecuteSkill(context.Background(), actorlayer.Envelope{ID: "command-id"}, testSkillCommandPayload(), commandskill.Selector{Name: "review"}, "")
	if !errors.Is(err, commandskill.ErrRevisionUnavailable) {
		t.Fatalf("ExecuteSkill() error = %v, want revision unavailable", err)
	}
	if len(dispatcher.envelopes) != 0 {
		t.Errorf("child turns = %d, want 0", len(dispatcher.envelopes))
	}
}

func TestSkillTurnExecutorReportsRuntimeAndDispatchFailures(t *testing.T) {
	payload := testSkillCommandPayload()
	parent := actorlayer.Envelope{ID: "command-id"}
	selector := commandskill.Selector{Name: "review"}

	if err := NewSkillTurnExecutor(nil, &skillTurnDispatcher{}).ExecuteSkill(context.Background(), parent, payload, selector, ""); !errors.Is(err, commandskill.ErrRuntimeUnavailable) {
		t.Fatalf("nil manager error = %v, want runtime unavailable", err)
	}

	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "default"}
	manager := newTestSkillManager(t, skillSnapshot(skillMetadata(source, "review", "revision")), &unreadSkillContent{})
	if err := NewSkillTurnExecutor(manager, nil).ExecuteSkill(context.Background(), parent, payload, selector, ""); !errors.Is(err, commandskill.ErrRuntimeUnavailable) {
		t.Fatalf("nil dispatcher error = %v, want runtime unavailable", err)
	}

	dispatchErr := errors.New("dispatch failed")
	err := NewSkillTurnExecutor(manager, &skillTurnDispatcher{err: dispatchErr}).ExecuteSkill(context.Background(), parent, payload, selector, "")
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("dispatch error = %v, want wrapped dispatch failure", err)
	}

	err = NewSkillTurnExecutor(manager, &skillTurnDispatcher{}).ExecuteSkill(context.Background(), parent, payload, selector, "")
	if err == nil {
		t.Fatal("nil receipt was accepted")
	}
}

func newTestSkillManager(t *testing.T, snapshot runtimecatalogcmd.Snapshot, reader *unreadSkillContent) *baldaagent.SkillManager {
	t.Helper()
	manager, err := baldaagent.NewSkillManager(&retainedSkillCatalog{snapshot: snapshot}, reader, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatalf("NewSkillManager() error = %v", err)
	}
	return manager
}

func skillSnapshot(skills ...runtimecatalogcmd.SkillMetadata) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{
		ID:     "snapshot-one",
		Skills: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata, len(skills)),
	}
	for _, skill := range skills {
		snapshot.Skills[skill.ID] = skill
	}
	return snapshot
}

func skillMetadata(source runtimecatalogcmd.SourceID, name string, revision runtimecatalogcmd.RevisionID) runtimecatalogcmd.SkillMetadata {
	return runtimecatalogcmd.SkillMetadata{
		ID: runtimecatalogcmd.ContributionID{
			Source: source,
			Kind:   runtimecatalogcmd.ContributionKindSkill,
			Name:   name,
		},
		Revision: revision,
		Name:     name,
		Resource: "skills/" + name + "/SKILL.md",
	}
}

func testSkillCommandPayload() commandcmd.Payload {
	return commandcmd.Payload{
		Version:    commandcmd.SchemaVersion,
		Name:       "skill",
		SnapshotID: "snapshot-one",
		Locator:    deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "session-one"},
		Transport:  "telegram",
		Principal:  "telegram:42",
		Access:     commandcmd.Access{SessionCommands: true},
		Presentation: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
			ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: true},
		},
		Invocation: commandcmd.Invocation{Root: "/"},
	}
}
