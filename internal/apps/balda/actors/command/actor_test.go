package command

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
)

type recordingHandler struct {
	name  string
	calls int
}

func (h *recordingHandler) Name() string { return h.name }
func (h *recordingHandler) Handle(context.Context, actorlayer.Envelope, commandcmd.Payload) error {
	h.calls++
	return nil
}

type recordingSnapshotResolver struct {
	snapshots map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
	ids       []runtimecatalogcmd.SnapshotID
	err       error
}

func (r *recordingSnapshotResolver) ResolveCommandSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	r.ids = append(r.ids, id)
	if r.err != nil {
		return runtimecatalogcmd.Snapshot{}, r.err
	}
	snapshot, ok := r.snapshots[id]
	if !ok {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	return snapshot, nil
}

func TestActorResolvesExactPinnedSnapshotOnRetry(t *testing.T) {
	t.Parallel()

	handler := &recordingHandler{name: "help"}
	router, err := NewRouter([]Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &recordingSnapshotResolver{snapshots: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{
		"snapshot-old": {ID: "snapshot-old"},
		"snapshot-new": {ID: "snapshot-new"},
	}}
	actor := NewActor(router, resolver)
	env := commandEnvelope(t, commandcmd.SchemaVersion, "help", "snapshot-old")
	if err := actor.Handle(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if err := actor.Handle(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if handler.calls != 2 || len(resolver.ids) != 2 || resolver.ids[0] != "snapshot-old" || resolver.ids[1] != "snapshot-old" {
		t.Fatalf("calls/resolved IDs = %d/%+v", handler.calls, resolver.ids)
	}
}

func TestActorReturnsStableUnavailableRevision(t *testing.T) {
	t.Parallel()

	handler := &recordingHandler{name: "help"}
	router, err := NewRouter([]Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	actor := NewActor(router, &recordingSnapshotResolver{snapshots: map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot{}})
	err = actor.Handle(context.Background(), commandEnvelope(t, commandcmd.SchemaVersion, "help", "missing"))
	if !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) || handler.calls != 0 {
		t.Fatalf("Handle() error/calls = %v/%d", err, handler.calls)
	}
}

func TestActorKeepsSnapshotLookupFailureRetryable(t *testing.T) {
	t.Parallel()

	handler := &recordingHandler{name: "help"}
	router, err := NewRouter([]Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("temporary catalog failure")
	actor := NewActor(router, &recordingSnapshotResolver{err: lookupErr})
	err = actor.Handle(context.Background(), commandEnvelope(t, commandcmd.SchemaVersion, "help", "snapshot"))
	if !errors.Is(err, lookupErr) || actorlayer.ClassifyError(err) != actorlayer.ErrorKindTransient || handler.calls != 0 {
		t.Fatalf("Handle() error/calls = %v/%d, want retryable lookup failure", err, handler.calls)
	}
}

func TestActorAllowsLegacyBuiltinsOnly(t *testing.T) {
	t.Parallel()

	handler := &recordingHandler{name: "help"}
	router, err := NewRouter([]Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	actor := NewActor(router, nil)
	if err := actor.Handle(context.Background(), commandEnvelope(t, commandcmd.LegacySchemaVersion, "help", "")); err != nil {
		t.Fatalf("legacy builtin error = %v", err)
	}
	if err := actor.Handle(context.Background(), commandEnvelope(t, commandcmd.LegacySchemaVersion, "plugin-command", "")); err == nil {
		t.Fatal("legacy non-builtin command was accepted")
	}
}

func commandEnvelope(t *testing.T, version int, name string, snapshot runtimecatalogcmd.SnapshotID) actorlayer.Envelope {
	t.Helper()
	payload := commandcmd.Payload{
		Version: version, Name: name, SnapshotID: snapshot, Transport: "telegram", Principal: "tg:1",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "s-1"},
	}
	if version == commandcmd.SchemaVersion {
		env, err := commandcmd.NewEnvelope(payload, commandcmd.EnvelopeOptions{ID: "command-1", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}})
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	body, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return actorlayer.Envelope{
		ID: "legacy-1", Namespace: actorcmd.NamespaceChatCommand, Kind: actorcmd.KindCommandExecute,
		From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"},
		To:   actorlayer.ActorAddress{Target: actorcmd.ActorTypeCommand, Key: "s-1"}, Payload: body,
	}
}
