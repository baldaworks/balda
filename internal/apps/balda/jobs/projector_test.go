package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	baldaruntime "github.com/normahq/balda/internal/apps/balda/runtime"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/pkg/actorlayer"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
)

func TestNewEventProjectorRequiresConsumer(t *testing.T) {
	t.Parallel()

	projector, err := NewEventProjector(eventProjectorParams{
		LC:            fxtest.NewLifecycle(t),
		StateProvider: newEventProjectorStateProvider(t, context.Background()),
		Logger:        zerolog.Nop(),
	})
	if err == nil || !strings.Contains(err.Error(), "actor runtime event consumer") {
		t.Fatalf("NewEventProjector() = (%v, %v), want consumer error", projector, err)
	}
}

func TestEventProjectorProjectsTaskEventIdempotently(t *testing.T) {
	ctx := context.Background()
	provider := newEventProjectorStateProvider(t, ctx)
	projector := &EventProjector{store: provider.Swarm(), logger: zerolog.Nop()}
	env := actorlayer.Envelope{
		ID:          "event-1",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-1"},
		TaskID:      "task-1",
		PayloadJSON: `{"text":"working"}`,
		Meta:        map[string]string{"event_type": TaskEventAgentProgress, "actor": "agent:executor", "message_id": "msg-1"},
	}
	if err := projector.Project(ctx, baldaruntime.SubjectEventJobUpdated, env); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if err := projector.Project(ctx, baldaruntime.SubjectEventJobUpdated, env); err != nil {
		t.Fatalf("Project(duplicate) error = %v", err)
	}
	events, err := provider.Swarm().ListTaskEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != TaskEventAgentProgress || events[0].Actor != "agent:executor" {
		t.Fatalf("events = %+v, want one projected job event", events)
	}
}

func TestEventProjectorProjectsCommandEventForTask(t *testing.T) {
	ctx := context.Background()
	provider := newEventProjectorStateProvider(t, ctx)
	projector := &EventProjector{store: provider.Swarm(), logger: zerolog.Nop()}
	env := actorlayer.Envelope{
		ID:          "cmd-1:event:deadlettered",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "command_event",
		From:        actorlayer.SystemAddress("transport"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-1"},
		TaskID:      "task-1",
		PayloadJSON: `{"reason":"retry exhausted"}`,
	}
	if err := projector.Project(ctx, baldaruntime.SubjectEventCommandDeadLettered, env); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	events, err := provider.Swarm().ListTaskEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != "command.deadlettered" {
		t.Fatalf("events = %+v, want command.deadlettered projection", events)
	}
}

func TestEventProjectorProjectsCommandDecodeFailedEventForTask(t *testing.T) {
	ctx := context.Background()
	provider := newEventProjectorStateProvider(t, ctx)
	projector := &EventProjector{store: provider.Swarm(), logger: zerolog.Nop()}
	env := actorlayer.Envelope{
		ID:          "cmd-1:event:decode_failed",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "command_event",
		From:        actorlayer.SystemAddress("transport"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-1"},
		TaskID:      "task-1",
		PayloadJSON: `{"reason":"decode failed: invalid json"}`,
	}
	if err := projector.Project(ctx, baldaruntime.SubjectEventCommandDecodeFailed, env); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	events, err := provider.Swarm().ListTaskEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != "command.decode_failed" {
		t.Fatalf("events = %+v, want command.decode_failed projection", events)
	}
}

func TestEventProjectorProjectsDeliveryFailedEventForTask(t *testing.T) {
	ctx := context.Background()
	provider := newEventProjectorStateProvider(t, ctx)
	projector := &EventProjector{store: provider.Swarm(), logger: zerolog.Nop()}
	env := actorlayer.Envelope{
		ID:          "delivery-1:event:failed",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-1"},
		TaskID:      "task-1",
		PayloadJSON: `{"reason":"telegram send failed"}`,
	}
	if err := projector.Project(ctx, baldaruntime.SubjectEventDeliveryFailed, env); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	events, err := provider.Swarm().ListTaskEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != TaskEventDeliveryFailed {
		t.Fatalf("events = %+v, want delivery.failed projection", events)
	}
}

func TestEventProjectorReplayAfterRestartRemainsIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	providerA := newEventProjectorStateProviderAtPath(t, ctx, dbPath)
	projectorA := &EventProjector{store: providerA.Swarm(), logger: zerolog.Nop()}
	eventCreated := actorlayer.Envelope{
		ID:          "evt-task-created",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-replay"},
		TaskID:      "task-replay",
		PayloadJSON: `{"status":"created"}`,
		Meta:        map[string]string{"event_type": JobEventCreated, "actor": "task:actor", "message_id": "m-1"},
	}
	eventProgress := actorlayer.Envelope{
		ID:          "evt-task-progress",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-replay"},
		TaskID:      "task-replay",
		PayloadJSON: `{"status":"running"}`,
		Meta:        map[string]string{"event_type": TaskEventAgentProgress, "actor": "agent:executor", "message_id": "m-2"},
	}
	if err := projectorA.Project(ctx, baldaruntime.SubjectEventJobCreated, eventCreated); err != nil {
		t.Fatalf("Project(created) error = %v", err)
	}
	if err := projectorA.Project(ctx, baldaruntime.SubjectEventJobUpdated, eventProgress); err != nil {
		t.Fatalf("Project(progress) error = %v", err)
	}
	if err := providerA.Close(); err != nil {
		t.Fatalf("providerA.Close() error = %v", err)
	}

	providerB := newEventProjectorStateProviderAtPath(t, ctx, dbPath)
	projectorB := &EventProjector{store: providerB.Swarm(), logger: zerolog.Nop()}
	eventCompleted := actorlayer.Envelope{
		ID:          "evt-task-completed",
		Namespace:   baldaruntime.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeJob, Key: "task-replay"},
		TaskID:      "task-replay",
		PayloadJSON: `{"status":"completed"}`,
		Meta:        map[string]string{"event_type": JobEventCompleted, "actor": "task:actor", "message_id": "m-3"},
	}
	if err := projectorB.Project(ctx, baldaruntime.SubjectEventJobCreated, eventCreated); err != nil {
		t.Fatalf("Project(replay created) error = %v", err)
	}
	if err := projectorB.Project(ctx, baldaruntime.SubjectEventJobUpdated, eventProgress); err != nil {
		t.Fatalf("Project(replay progress) error = %v", err)
	}
	if err := projectorB.Project(ctx, baldaruntime.SubjectEventJobCompleted, eventCompleted); err != nil {
		t.Fatalf("Project(completed) error = %v", err)
	}

	events, err := providerB.Swarm().ListTaskEvents(ctx, "task-replay")
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("projected replay events len = %d, want 3", len(events))
	}
	if events[0].ID != eventCreated.ID || events[1].ID != eventProgress.ID || events[2].ID != eventCompleted.ID {
		t.Fatalf("projected replay event IDs = [%s %s %s], want [%s %s %s]", events[0].ID, events[1].ID, events[2].ID, eventCreated.ID, eventProgress.ID, eventCompleted.ID)
	}
	if events[0].EventType != JobEventCreated || events[1].EventType != TaskEventAgentProgress || events[2].EventType != JobEventCompleted {
		t.Fatalf("projected replay event types = [%s %s %s], want [%s %s %s]", events[0].EventType, events[1].EventType, events[2].EventType, JobEventCreated, TaskEventAgentProgress, JobEventCompleted)
	}
}

func newEventProjectorStateProvider(t *testing.T, ctx context.Context) baldastate.Provider {
	t.Helper()

	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func newEventProjectorStateProviderAtPath(t *testing.T, ctx context.Context, path string) baldastate.Provider {
	t.Helper()

	provider, err := baldastate.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(%s) error = %v", path, err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}
