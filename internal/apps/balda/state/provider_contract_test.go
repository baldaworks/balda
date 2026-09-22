//go:build integration && (sqlite || postgres)

package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/sessionmemory"
	adksession "google.golang.org/adk/v2/session"
)

const firstPluginRevision = "rev-1"
const secondPluginRevision = "rev-2"

type contractOpener func(context.Context, string) (Provider, error)

func runProviderContract(t *testing.T, factory func(*testing.T) contractOpener) {
	t.Run("UserStoreCanonicalLifecycle", func(t *testing.T) { checkUserStoreCanonicalLifecycle(t, factory(t)) })
	t.Run("UserStoreRefreshRotationAndReplay", func(t *testing.T) { checkUserStoreRefreshRotationAndReplay(t, factory(t)) })
	t.Run("UserStoreCredentialAndSessionRevocation", func(t *testing.T) { checkUserStoreCredentialAndSessionRevocation(t, factory(t)) })
	t.Run("UserStoreConcurrentRefreshReplay", func(t *testing.T) { checkUserStoreConcurrentRefreshReplay(t, factory(t)) })
	t.Run("Collaborators", func(t *testing.T) { checkCollaborators(t, factory(t)) })
	t.Run("Provider_KVRoundTrip", func(t *testing.T) { checkProvider_KVRoundTrip(t, factory(t)) })
	t.Run("Provider_KVConsumeJSONConcurrentConsumeOnce", func(t *testing.T) { checkProvider_KVConsumeJSONConcurrentConsumeOnce(t, factory(t)) })
	t.Run("Provider_SessionStoreRoundTrip", func(t *testing.T) { checkProvider_SessionStoreRoundTrip(t, factory(t)) })
	t.Run("Provider_SessionStoreUpsert_AllowsMultipleTelegramSessions", func(t *testing.T) { checkProvider_SessionStoreUpsert_AllowsMultipleTelegramSessions(t, factory(t)) })
	t.Run("Provider_QuestionStoreRoundTrip", func(t *testing.T) { checkProvider_QuestionStoreRoundTrip(t, factory(t)) })
	t.Run("Provider_SessionStoreUpsert_DoesNotDecodeAddressJSON", func(t *testing.T) { checkProvider_SessionStoreUpsert_DoesNotDecodeAddressJSON(t, factory(t)) })
	t.Run("Provider_ScheduledJobStoreRoundTrip", func(t *testing.T) { checkProvider_ScheduledJobStoreRoundTrip(t, factory(t)) })
	t.Run("Provider_OffsetPersistsAcrossReopen", func(t *testing.T) { checkProvider_OffsetPersistsAcrossReopen(t, factory(t)) })
	t.Run("Provider_RuntimeSessionPersistsAcrossReopen", func(t *testing.T) { checkProvider_RuntimeSessionPersistsAcrossReopen(t, factory(t)) })
	t.Run("JobStore_JobLifecycle", func(t *testing.T) { checkJobStore_JobLifecycle(t, factory(t)) })
	t.Run("JobStore_JobStatusTransitionsAreGuarded", func(t *testing.T) { checkJobStore_JobStatusTransitionsAreGuarded(t, factory(t)) })
	t.Run("JobStore_JobMutationAndEventOutboxAreAtomic", func(t *testing.T) { checkJobStore_JobMutationAndEventOutboxAreAtomic(t, factory(t)) })
	t.Run("JobStore_SetJobResultTransitionGuarded", func(t *testing.T) { checkJobStore_SetJobResultTransitionGuarded(t, factory(t)) })
	t.Run("JobStore_DeliveryOutboxLifecycle", func(t *testing.T) { checkJobStore_DeliveryOutboxLifecycle(t, factory(t)) })
	t.Run("JobStore_AgentStepLifecycle", func(t *testing.T) { checkJobStore_AgentStepLifecycle(t, factory(t)) })
	t.Run("PluginStoreActivationSurvivesRestart", func(t *testing.T) { checkPluginStoreActivationSurvivesRestart(t, factory(t)) })
	t.Run("PluginStoreActivationIsAtomicAndOriginLocked", func(t *testing.T) { checkPluginStoreActivationIsAtomicAndOriginLocked(t, factory(t)) })
	t.Run("PluginStoreRetainsAndSafelyPurgesRevisions", func(t *testing.T) { checkPluginStoreRetainsAndSafelyPurgesRevisions(t, factory(t)) })
	t.Run("PluginStoreRejectsConflictingRevisionAndTraversal", func(t *testing.T) { checkPluginStoreRejectsConflictingRevisionAndTraversal(t, factory(t)) })
	t.Run("SessionMemoryIngressOutboxPersistsFIFOClaimsAcrossRestart", func(t *testing.T) { checkSessionMemoryIngressOutboxPersistsFIFOClaimsAcrossRestart(t, factory(t)) })
	t.Run("SessionMemoryIngressOutboxReplaysTypedToolEvidenceWithoutDuplication", func(t *testing.T) {
		checkSessionMemoryIngressOutboxReplaysTypedToolEvidenceWithoutDuplication(t, factory(t))
	})
	t.Run("SessionMemoryIngressOutboxRecoversExpiredLeaseAndRejectsForeignSettlement", func(t *testing.T) {
		checkSessionMemoryIngressOutboxRecoversExpiredLeaseAndRejectsForeignSettlement(t, factory(t))
	})
	t.Run("SessionMemoryIngressOutboxReplaysTerminalWithAuditAndStats", func(t *testing.T) { checkSessionMemoryIngressOutboxReplaysTerminalWithAuditAndStats(t, factory(t)) })
}

func checkCollaborators(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "state.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := authcmd.Collaborator{UserID: "telegram:12345", Username: "alice", FirstName: "Alice", AddedBy: "owner", AddedAt: time.Now().UTC().Truncate(time.Second)}
	if err := p.Collaborators().AddCollaborator(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	want.Username = "alice-updated"
	if err := p.Collaborators().AddCollaborator(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeContractProvider(t, p)
	got, found, err := p.Collaborators().GetCollaborator(t.Context(), want.UserID)
	if err != nil || !found || got == nil || *got != want {
		t.Fatalf("reopened collaborator = %v, %v, %v", got, found, err)
	}
	all, err := p.Collaborators().ListCollaborators(t.Context())
	if err != nil || len(all) != 1 || all[0] != want {
		t.Fatalf("collaborators = %v, %v", all, err)
	}
	if err := p.Collaborators().RemoveCollaborator(t.Context(), want.UserID); err != nil {
		t.Fatal(err)
	}
	_, found, err = p.Collaborators().GetCollaborator(t.Context(), want.UserID)
	if err != nil || found {
		t.Fatalf("removed collaborator found=%v, error=%v", found, err)
	}
}

func checkProvider_KVRoundTrip(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.SessionMCPKV()

	if err := store.Set(ctx, "alpha", "one"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, ok, err := store.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() found = false, want true")
	}
	if got != "one" {
		t.Fatalf("Get() value = %q, want %q", got, "one")
	}

	if err := store.SetJSON(ctx, "json", map[string]any{"count": 2}); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}
	merged, err := store.MergeJSON(ctx, "json", map[string]any{"name": "balda"})
	if err != nil {
		t.Fatalf("MergeJSON() error = %v", err)
	}
	if merged["count"] != float64(2) {
		t.Fatalf("merged[count] = %v, want 2", merged["count"])
	}
	if merged["name"] != "balda" {
		t.Fatalf("merged[name] = %v, want balda", merged["name"])
	}
}

func checkProvider_KVConsumeJSONConcurrentConsumeOnce(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.AppKV()
	if err := store.SetJSON(ctx, "token", map[string]any{"channel": "slack"}); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, consumed, err := store.ConsumeJSON(ctx, "token", func(value any) (bool, error) {
				return true, nil
			})
			if err != nil {
				t.Errorf("ConsumeJSON() error = %v", err)
				return
			}
			if consumed {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumes = %d, want 1", got)
	}
	_, ok, err := store.GetJSON(ctx, "token")
	if err != nil {
		t.Fatalf("GetJSON() error = %v", err)
	}
	if ok {
		t.Fatal("GetJSON() found token after consume, want false")
	}
}

func checkProvider_SessionStoreRoundTrip(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Sessions()

	record := SessionRecord{
		SessionID:         "tg-1-2",
		UserID:            "tg-101",
		ChannelType:       ChannelTypeTelegram,
		AddressKey:        "1:2",
		AddressJSON:       `{"chat_id":1,"topic_id":2}`,
		AgentName:         "agent",
		WorkspaceDir:      "/tmp/ws",
		BranchName:        "norma/balda/tg-1-2",
		RuntimeSnapshotID: "snapshot-1",
		Status:            SessionStatusActive,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "1:2")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true")
	}
	if got.SessionID != record.SessionID {
		t.Fatalf("session_id = %q, want %q", got.SessionID, record.SessionID)
	}
	if got.AgentName != record.AgentName {
		t.Fatalf("agent_name = %q, want %q", got.AgentName, record.AgentName)
	}
	if got.UserID != record.UserID {
		t.Fatalf("user_id = %q, want %q", got.UserID, record.UserID)
	}
	if got.RuntimeSnapshotID != record.RuntimeSnapshotID {
		t.Fatalf("runtime_snapshot_id = %q, want %q", got.RuntimeSnapshotID, record.RuntimeSnapshotID)
	}
}

func checkProvider_SessionStoreUpsert_AllowsMultipleTelegramSessions(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Sessions()

	records := []SessionRecord{
		{
			SessionID:    "tg--1002667079342-8939",
			UserID:       "tg-101",
			ChannelType:  ChannelTypeTelegram,
			AddressKey:   "-1002667079342:8939",
			AddressJSON:  `{"chat_id":-1002667079342,"topic_id":8939}`,
			AgentName:    "agent",
			WorkspaceDir: "/tmp/ws-1",
			BranchName:   "norma/balda/tg--1002667079342-8939",
			Status:       SessionStatusActive,
		},
		{
			SessionID:    "tg--1002667079342-8940",
			UserID:       "tg-101",
			ChannelType:  ChannelTypeTelegram,
			AddressKey:   "-1002667079342:8940",
			AddressJSON:  `{"chat_id":-1002667079342,"topic_id":8940}`,
			AgentName:    "agent",
			WorkspaceDir: "/tmp/ws-2",
			BranchName:   "norma/balda/tg--1002667079342-8940",
			Status:       SessionStatusActive,
		},
	}
	for _, record := range records {
		if err := store.Upsert(ctx, record); err != nil {
			t.Fatalf("Upsert(%q) error = %v", record.SessionID, err)
		}
	}
}

func checkProvider_QuestionStoreRoundTrip(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Questions()

	record := QuestionRecord{
		QuestionID:      "question-1",
		SessionID:       "tg-1-0",
		ChannelKind:     ChannelTypeTelegram,
		AddressKey:      "1:0",
		AddressJSON:     `{"chat_id":1,"topic_id":0}`,
		Prompt:          "continue?",
		Status:          questioncmd.StatusPending,
		InteractionJSON: `{"session_id":"tg-1-0"}`,
		ResumeJSON:      `{"to":"goalkeeper:job-1"}`,
		RequestJSON:     `{"prompt":"continue?"}`,
	}
	if err := store.CreatePendingQuestion(ctx, record); err != nil {
		t.Fatalf("CreatePendingQuestion() error = %v", err)
	}
	if err := store.BindQuestionDeliveryRef(ctx, record.QuestionID, questioncmd.DeliveryRef{
		Provider:          "telegram",
		ConversationKey:   "1:0",
		ProviderMessageID: "42",
	}); err != nil {
		t.Fatalf("BindQuestionDeliveryRef() error = %v", err)
	}
	got, ok, err := store.GetPendingQuestionByReplyRef(ctx, "telegram", "1:0", "42")
	if err != nil {
		t.Fatalf("GetPendingQuestionByReplyRef() error = %v", err)
	}
	if !ok {
		t.Fatal("GetPendingQuestionByReplyRef() found = false, want true")
	}
	if got.QuestionID != record.QuestionID {
		t.Fatalf("question_id = %q, want %q", got.QuestionID, record.QuestionID)
	}
	answered, settled, err := store.MarkQuestionAnswered(ctx, record.QuestionID, questioncmd.Answer{
		Text:       "yes",
		AnsweredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("MarkQuestionAnswered() error = %v", err)
	}
	if !settled {
		t.Fatal("MarkQuestionAnswered() settled = false, want true")
	}
	if answered.Status != questioncmd.StatusAnswered {
		t.Fatalf("status = %q, want answered", answered.Status)
	}
}

func checkProvider_SessionStoreUpsert_DoesNotDecodeAddressJSON(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Sessions()

	record := SessionRecord{
		SessionID:    "tg-9-9",
		UserID:       "tg-900",
		ChannelType:  ChannelTypeTelegram,
		AddressKey:   "9:9",
		AddressJSON:  "{",
		AgentName:    "agent",
		WorkspaceDir: "/tmp/ws",
		BranchName:   "norma/balda/tg-9-9",
		Status:       SessionStatusActive,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "9:9")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true")
	}
	if got.AddressJSON != record.AddressJSON {
		t.Fatalf("address_json = %q, want %q", got.AddressJSON, record.AddressJSON)
	}
}

func checkProvider_ScheduledJobStoreRoundTrip(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.ScheduledJobs()
	nextRunAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	const scheduledJobID = "task-1"

	record := ScheduledJobRecord{
		JobID:               scheduledJobID,
		SessionID:           "tg-1-2",
		ChannelType:         ChannelTypeTelegram,
		AddressKey:          "1:2",
		AddressJSON:         `{"chat_id":1,"topic_id":2}`,
		ReportToEnabled:     true,
		ReportToSessionID:   "tg-1-0",
		ReportToChannelType: ChannelTypeTelegram,
		ReportToAddressKey:  "1:0",
		ReportToAddressJSON: `{"chat_id":1,"topic_id":0}`,
		Content:             "check deployment",
		ScheduleSpec:        "*/5 * * * *",
		Timezone:            "UTC",
		Status:              ScheduledJobStatusActive,
		MaxRetries:          4,
		NextRunAt:           nextRunAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByID(ctx, scheduledJobID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() found = false, want true")
	}
	if got.Content != record.Content {
		t.Fatalf("content = %q, want %q", got.Content, record.Content)
	}
	if got.ScheduleSpec != record.ScheduleSpec {
		t.Fatalf("schedule_spec = %q, want %q", got.ScheduleSpec, record.ScheduleSpec)
	}
	if !got.ReportToEnabled || got.ReportToAddressKey != record.ReportToAddressKey {
		t.Fatalf("report_to fields = %+v, want enabled address %q", got, record.ReportToAddressKey)
	}
	allTasks, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(allTasks) != 1 || allTasks[0].JobID != scheduledJobID {
		t.Fatalf("List() = %#v, want single %s", allTasks, scheduledJobID)
	}

	dueTasks, err := store.ListDue(ctx, nextRunAt.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("ListDue() error = %v", err)
	}
	if len(dueTasks) != 1 || dueTasks[0].JobID != scheduledJobID {
		t.Fatalf("ListDue() = %#v, want single %s", dueTasks, scheduledJobID)
	}

	if err := store.Delete(ctx, scheduledJobID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	_, ok, err = store.GetByID(ctx, scheduledJobID)
	if err != nil {
		t.Fatalf("GetByID(after delete) error = %v", err)
	}
	if ok {
		t.Fatal("GetByID(after delete) found = true, want false")
	}
}

func checkProvider_OffsetPersistsAcrossReopen(t *testing.T, open contractOpener) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	providerA, err := open(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(A) error = %v", err)
	}
	if err := providerA.PollingOffsetStore().Save(ctx, 99); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	closeContractProvider(t, providerA)

	providerB, err := open(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(B) error = %v", err)
	}
	defer closeContractProvider(t, providerB)

	offset, err := providerB.PollingOffsetStore().Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if offset != 99 {
		t.Fatalf("offset = %d, want 99", offset)
	}
}

func checkProvider_RuntimeSessionPersistsAcrossReopen(t *testing.T, open contractOpener) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	providerA, err := open(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(A) error = %v", err)
	}
	svcA := providerA.RuntimeSessions()
	created, err := svcA.Create(ctx, &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-1-2",
		State: map[string]any{
			"cwd":        "/workspace",
			"app:shared": "app-value",
			"user:name":  "owner",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := adksession.NewEvent(context.Background(), "invocation-1")
	event.Author = "user"
	event.IsolationScope = "scope-1"
	event.Routes = []string{"start", "worker", "done"}
	event.RequestedInput = &adksession.RequestInput{InterruptID: "approve-1", Message: "Approve?"}
	event.Output = map[string]any{"result": "ok"}
	event.NodeInfo = &adksession.NodeInfo{
		Path:            "goal/worker",
		MessageAsOutput: true,
		OutputFor:       []string{"goal"},
	}
	event.Actions.StateDelta = map[string]any{
		"cwd":       "/workspace/session",
		"temp:skip": "drop",
	}
	if err := svcA.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	closeContractProvider(t, providerA)

	providerB, err := open(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(B) error = %v", err)
	}
	defer closeContractProvider(t, providerB)

	got, err := providerB.RuntimeSessions().Get(ctx, &adksession.GetRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-1-2",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session.Events().Len() != 1 {
		t.Fatalf("events len = %d, want 1", got.Session.Events().Len())
	}
	gotEvent := got.Session.Events().At(0)
	if gotEvent.IsolationScope != "scope-1" {
		t.Fatalf("event IsolationScope = %q, want scope-1", gotEvent.IsolationScope)
	}
	if len(gotEvent.Routes) != 3 || gotEvent.Routes[1] != "worker" {
		t.Fatalf("event Routes = %#v, want start/worker/done", gotEvent.Routes)
	}
	if gotEvent.RequestedInput == nil || gotEvent.RequestedInput.InterruptID != "approve-1" {
		t.Fatalf("event RequestedInput = %#v, want approve-1", gotEvent.RequestedInput)
	}
	output, ok := gotEvent.Output.(map[string]any)
	if !ok || output["result"] != "ok" {
		t.Fatalf("event Output = %#v, want result ok", gotEvent.Output)
	}
	if gotEvent.NodeInfo == nil || gotEvent.NodeInfo.Path != "goal/worker" || !gotEvent.NodeInfo.MessageAsOutput {
		t.Fatalf("event NodeInfo = %#v, want goal/worker message output", gotEvent.NodeInfo)
	}
	if len(gotEvent.NodeInfo.OutputFor) != 1 || gotEvent.NodeInfo.OutputFor[0] != "goal" {
		t.Fatalf("event NodeInfo.OutputFor = %#v, want [goal]", gotEvent.NodeInfo.OutputFor)
	}
	if value, err := got.Session.State().Get("cwd"); err != nil || value != "/workspace/session" {
		t.Fatalf("state cwd = %v, err = %v, want /workspace/session", value, err)
	}
	if value, err := got.Session.State().Get("app:shared"); err != nil || value != "app-value" {
		t.Fatalf("state app:shared = %v, err = %v, want app-value", value, err)
	}
	if _, err := got.Session.State().Get("temp:skip"); err == nil {
		t.Fatal("temp state key persisted, want missing key")
	}
}

func checkJobStore_JobLifecycle(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Jobs()

	created, err := store.CreateJob(ctx, JobRecord{
		ID:            "task-1",
		SessionID:     "session-1",
		Title:         "Goal: test",
		Objective:     "test",
		Status:        JobStatusCreated,
		AssignedActor: "agent:executor",
		CreatedBy:     "tg-101",
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if !created {
		t.Fatal("CreateJob() created = false, want true")
	}
	created, err = store.CreateJob(ctx, JobRecord{
		ID:        "task-1",
		Objective: "duplicate",
	})
	if err != nil {
		t.Fatalf("CreateJob(duplicate) error = %v", err)
	}
	if created {
		t.Fatal("CreateJob(duplicate) created = true, want false")
	}

	if err := store.UpdateJobStatus(ctx, "task-1", JobStatusWaitingForAgent, "waiting"); err != nil {
		t.Fatalf("UpdateJobStatus(waiting) error = %v", err)
	}
	if err := store.AppendJobEvent(ctx, JobEventRecord{
		ID:        "event-1",
		JobID:     "task-1",
		EventType: "agent.started",
		Actor:     "task.actor",
		Payload:   `{"role":"executor"}`,
	}); err != nil {
		t.Fatalf("AppendJobEvent() error = %v", err)
	}

	active, err := store.ListActiveJobsBySession(ctx, "session-1")
	if err != nil {
		t.Fatalf("ListActiveJobsBySession() error = %v", err)
	}
	if len(active) != 1 || active[0].ID != "task-1" {
		t.Fatalf("active tasks = %+v, want task-1", active)
	}

	if err := store.SetJobResult(ctx, "task-1", `{"ok":true}`, JobStatusCompleted, ""); err != nil {
		t.Fatalf("SetJobResult() error = %v", err)
	}
	got, ok, err := store.GetJob(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if !ok || got.Status != JobStatusCompleted || got.Result == "" || got.StartedAt.IsZero() || got.CompletedAt.IsZero() {
		t.Fatalf("job = %+v, found=%v, want completed with result/timestamps", got, ok)
	}

	active, err = store.ListActiveJobsBySession(ctx, "session-1")
	if err != nil {
		t.Fatalf("ListActiveJobsBySession(after complete) error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active tasks after complete = %+v, want none", active)
	}
	events, err := store.ListJobEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListJobEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != "agent.started" {
		t.Fatalf("events = %+v, want agent.started", events)
	}
}

func checkJobStore_JobStatusTransitionsAreGuarded(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Jobs()

	_, err := store.CreateJob(ctx, JobRecord{
		ID:        "task-guarded",
		SessionID: "session-1",
		Objective: "guard transitions",
		Status:    JobStatusRunning,
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if err := store.UpdateJobStatus(ctx, "task-guarded", JobStatusCompleted, "done"); err != nil {
		t.Fatalf("UpdateJobStatus(completed) error = %v", err)
	}

	err = store.UpdateJobStatus(ctx, "task-guarded", JobStatusRunning, "reopen")
	if err == nil || !strings.Contains(err.Error(), "invalid runtime job transition") {
		t.Fatalf("UpdateJobStatus(reopen) error = %v, want invalid transition", err)
	}
	got, ok, err := store.GetJob(ctx, "task-guarded")
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if !ok || got.Status != JobStatusCompleted {
		t.Fatalf("job = %+v found=%v, want status completed", got, ok)
	}

	if err := store.UpdateJobStatus(ctx, "task-guarded", JobStatusCompleted, "idempotent"); err != nil {
		t.Fatalf("UpdateJobStatus(idempotent terminal) error = %v", err)
	}
}

func checkJobStore_JobMutationAndEventOutboxAreAtomic(t *testing.T, open contractOpener) {
	t.Parallel()

	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)
	ctx := context.Background()
	store := provider.Jobs()

	created, err := store.CreateJobWithEvent(ctx, JobRecord{
		ID:        "job-outbox-1",
		SessionID: "session-1",
		Objective: "publish durable event",
		Status:    JobStatusCreated,
	}, JobEventOutboxRecord{
		ID:       "event-outbox-1",
		JobID:    "job-outbox-1",
		Subject:  "BALDA_EVENTS.job.created",
		Envelope: `{"id":"event-outbox-1"}`,
	})
	if err != nil {
		t.Fatalf("CreateJobWithEvent() error = %v", err)
	}
	if !created {
		t.Fatal("CreateJobWithEvent() created = false, want true")
	}
	pending, err := store.ListPendingJobEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingJobEvents() error = %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "event-outbox-1" {
		t.Fatalf("pending events = %+v, want event-outbox-1", pending)
	}

	invalidEvent := JobEventOutboxRecord{
		ID:       "event-outbox-invalid",
		JobID:    "job-outbox-1",
		Envelope: `{"id":"event-outbox-invalid"}`,
	}
	if err := store.UpdateJobStatusWithEvent(ctx, "job-outbox-1", JobStatusRunning, "", invalidEvent); err == nil {
		t.Fatal("UpdateJobStatusWithEvent(invalid event) error = nil, want validation error")
	}
	job, ok, err := store.GetJob(ctx, "job-outbox-1")
	if err != nil || !ok {
		t.Fatalf("GetJob() = %+v found=%v err=%v", job, ok, err)
	}
	if job.Status != JobStatusCreated {
		t.Fatalf("job status after rejected atomic mutation = %q, want %q", job.Status, JobStatusCreated)
	}

	if err := store.MarkJobEventPublishFailed(ctx, "event-outbox-1", "stream unavailable"); err != nil {
		t.Fatalf("MarkJobEventPublishFailed() error = %v", err)
	}
	pending, err = store.ListPendingJobEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingJobEvents(after failure) error = %v", err)
	}
	if len(pending) != 1 || pending[0].Attempts != 1 || pending[0].LastError != "stream unavailable" {
		t.Fatalf("pending event after failure = %+v, want attempts=1 with error", pending)
	}
	if err := store.MarkJobEventPublished(ctx, "event-outbox-1"); err != nil {
		t.Fatalf("MarkJobEventPublished() error = %v", err)
	}
	pending, err = store.ListPendingJobEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingJobEvents(after publish) error = %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending events after publish = %+v, want none", pending)
	}
}

func checkJobStore_SetJobResultTransitionGuarded(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Jobs()

	_, err := store.CreateJob(ctx, JobRecord{
		ID:        "task-canceled",
		SessionID: "session-1",
		Objective: "canceled",
		Status:    JobStatusCanceled,
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}

	err = store.SetJobResult(ctx, "task-canceled", `{"ok":true}`, JobStatusCompleted, "should fail")
	if err == nil || !strings.Contains(err.Error(), "invalid runtime job transition") {
		t.Fatalf("SetJobResult() error = %v, want invalid transition", err)
	}
	got, ok, err := store.GetJob(ctx, "task-canceled")
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if !ok || got.Status != JobStatusCanceled {
		t.Fatalf("job = %+v found=%v, want status canceled", got, ok)
	}
}

func checkJobStore_DeliveryOutboxLifecycle(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Jobs()

	record, created, err := store.ReserveDelivery(ctx, DeliveryRecord{
		ID:          "delivery-1",
		DeliveryKey: "task-1:delivery:started",
		JobID:       "task-1",
		SessionID:   "session-1",
		Channel:     "telegram",
		AddressKey:  "9001:1",
		Kind:        "delivery",
		Payload:     `{"text":"hello"}`,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveDelivery() error = %v", err)
	}
	if !created || record.Status != DeliveryStatusPending {
		t.Fatalf("ReserveDelivery() = %+v created=%v, want pending created", record, created)
	}

	again, created, err := store.ReserveDelivery(ctx, DeliveryRecord{
		ID:          "delivery-duplicate",
		DeliveryKey: "task-1:delivery:started",
		JobID:       "task-1",
		SessionID:   "session-1",
		Channel:     "telegram",
		AddressKey:  "9001:1",
		Kind:        "delivery",
		Payload:     `{"text":"hello"}`,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveDelivery(duplicate) error = %v", err)
	}
	if created || again.ID != "delivery-1" {
		t.Fatalf("ReserveDelivery(duplicate) = %+v created=%v, want existing", again, created)
	}

	if err := store.MarkDeliverySent(ctx, record.DeliveryKey, "tg-42"); err != nil {
		t.Fatalf("MarkDeliverySent() error = %v", err)
	}
	sent, created, err := store.ReserveDelivery(ctx, DeliveryRecord{
		ID:          "delivery-after-sent",
		DeliveryKey: record.DeliveryKey,
		JobID:       "task-1",
		SessionID:   "session-1",
		Channel:     "telegram",
		AddressKey:  "9001:1",
		Kind:        "delivery",
		Payload:     `{"text":"hello"}`,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveDelivery(after sent) error = %v", err)
	}
	if created || sent.Status != DeliveryStatusSent || sent.ProviderMessageID != "tg-42" || sent.SentAt.IsZero() {
		t.Fatalf("ReserveDelivery(after sent) = %+v created=%v, want sent existing", sent, created)
	}

	if err := store.MarkDeliveryFailed(ctx, record.DeliveryKey, "provider timeout"); err != nil {
		t.Fatalf("MarkDeliveryFailed() error = %v", err)
	}
	if delivery, created, err := store.ReserveDelivery(ctx, record); err != nil {
		t.Fatalf("ReserveDelivery(after failed) error = %v", err)
	} else if created || delivery.Status != DeliveryStatusFailed || delivery.Error != "provider timeout" {
		t.Fatalf("ReserveDelivery(after failed) = %+v created=%v, want failed existing", delivery, created)
	}
}

func checkJobStore_AgentStepLifecycle(t *testing.T, open contractOpener) {
	provider := newContractProvider(t, open)
	defer closeContractProvider(t, provider)

	ctx := context.Background()
	store := provider.Jobs()

	record, created, err := store.ReserveAgentStep(ctx, AgentStepRecord{
		ID:          "step-1",
		StepKey:     "task-1:agent:executor:executor:1",
		JobID:       "task-1",
		AgentName:   "executor",
		Role:        "executor",
		Iteration:   1,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveAgentStep() error = %v", err)
	}
	if !created || record.Status != AgentStepStatusRunning {
		t.Fatalf("ReserveAgentStep() = %+v created=%v, want running created", record, created)
	}

	again, created, err := store.ReserveAgentStep(ctx, AgentStepRecord{
		ID:          "step-duplicate",
		StepKey:     record.StepKey,
		JobID:       "task-1",
		AgentName:   "executor",
		Role:        "executor",
		Iteration:   1,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveAgentStep(duplicate) error = %v", err)
	}
	if created || again.ID != "step-1" || again.Status != AgentStepStatusRunning {
		t.Fatalf("ReserveAgentStep(duplicate) = %+v created=%v, want existing running", again, created)
	}

	if err := store.CompleteAgentStep(ctx, record.StepKey, `{"kind":"agent_result"}`); err != nil {
		t.Fatalf("CompleteAgentStep() error = %v", err)
	}
	completed, created, err := store.ReserveAgentStep(ctx, AgentStepRecord{
		ID:          "step-after-complete",
		StepKey:     record.StepKey,
		JobID:       "task-1",
		AgentName:   "executor",
		Role:        "executor",
		Iteration:   1,
		PayloadHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("ReserveAgentStep(after complete) error = %v", err)
	}
	if created || completed.Status != AgentStepStatusSucceeded || completed.Result == "" || completed.CompletedAt.IsZero() {
		t.Fatalf("ReserveAgentStep(after complete) = %+v created=%v, want stored succeeded result", completed, created)
	}
}

func checkPluginStoreActivationSurvivesRestart(t *testing.T, open contractOpener) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.db")
	provider, err := open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store := provider.Plugins()
	now := time.Date(2026, 9, 13, 5, 0, 0, 123, time.UTC)
	revision := PluginRevisionRecord{PluginID: "demo", RevisionID: "rev-1", Version: "1.0.0", RelativeRoot: "plugin-revisions/demo/rev-1", CapabilityJSON: `{"skills":1}`, CreatedAt: now}
	if err := store.PutPluginRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	intent := PluginActivationIntent{IntentID: "intent-1", PluginID: "demo", ToRevisionID: "rev-1", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	install := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "market", OriginSource: "https://example.com/repo.git", OriginPath: "plugins/demo", ActiveRevisionID: "rev-1", Enabled: true, Version: "1.0.0", CapabilityJSON: `{"skills":1}`, DataRelativePath: "plugin-data/demo", UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, install); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	provider, err = open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	stored, found, err := provider.Plugins().GetPluginInstall(ctx, "demo")
	if err != nil || !found {
		t.Fatalf("GetPluginInstall() = (%#v, %t, %v)", stored, found, err)
	}
	if stored != install {
		t.Fatalf("install = %#v, want %#v", stored, install)
	}
	incomplete, err := provider.Plugins().ListIncompletePluginActivations(ctx)
	if err != nil || len(incomplete) != 1 || incomplete[0].IntentID != "intent-1" {
		t.Fatalf("incomplete = %#v, err = %v", incomplete, err)
	}
	if err := provider.Plugins().CompletePluginActivation(ctx, "intent-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	incomplete, err = provider.Plugins().ListIncompletePluginActivations(ctx)
	if err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete after complete = %#v, err = %v", incomplete, err)
	}
}

func checkPluginStoreActivationIsAtomicAndOriginLocked(t *testing.T, open contractOpener) {
	t.Parallel()
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	store := provider.Plugins()
	now := time.Now().UTC()
	base := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "market", OriginSource: "source", OriginPath: "plugins/demo", ActiveRevisionID: "missing", Enabled: true, CapabilityJSON: `{}`, DataRelativePath: "plugin-data/demo", UpdatedAt: now}
	intent := PluginActivationIntent{IntentID: "missing", PluginID: "demo", ToRevisionID: "missing", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, base); err == nil {
		t.Fatal("ActivatePlugin() missing revision error = nil")
	}
	if pending, err := store.ListIncompletePluginActivations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, err = %v", pending, err)
	}

	for _, revisionID := range []string{firstPluginRevision, secondPluginRevision} {
		if err := store.PutPluginRevision(ctx, PluginRevisionRecord{PluginID: "demo", RevisionID: revisionID, RelativeRoot: "plugin-revisions/demo/" + revisionID, CapabilityJSON: `{}`, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	base.ActiveRevisionID = firstPluginRevision
	intent.IntentID = "install"
	intent.ToRevisionID = firstPluginRevision
	if err := store.ActivatePlugin(ctx, intent, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.OriginSource = "other"
	changed.ActiveRevisionID = secondPluginRevision
	upgrade := PluginActivationIntent{IntentID: "upgrade", PluginID: "demo", FromRevisionID: firstPluginRevision, ToRevisionID: secondPluginRevision, Operation: "upgrade", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, upgrade, changed); err == nil {
		t.Fatal("ActivatePlugin() origin change error = nil")
	}
	changed = base
	changed.ActiveRevisionID = "rev-2"
	changed.DataRelativePath = "other-data/demo"
	upgrade.IntentID = "move-data"
	if err := store.ActivatePlugin(ctx, upgrade, changed); err == nil {
		t.Fatal("ActivatePlugin() data path change error = nil")
	}
	stored, _, err := store.GetPluginInstall(ctx, "demo")
	if err != nil || stored.ActiveRevisionID != firstPluginRevision {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
	validInstall := base
	validInstall.ActiveRevisionID = secondPluginRevision
	validUpgrade := upgrade
	validUpgrade.IntentID = "upgrade-ok"
	var wait sync.WaitGroup
	wait.Add(2)
	var activationErr, purgeErr error
	go func() { defer wait.Done(); activationErr = store.ActivatePlugin(ctx, validUpgrade, validInstall) }()
	go func() { defer wait.Done(); purgeErr = store.PurgePluginRevision(ctx, "demo", firstPluginRevision) }()
	wait.Wait()
	if activationErr != nil || purgeErr == nil {
		t.Fatalf("concurrent activation error = %v, purge error = %v", activationErr, purgeErr)
	}
	if _, found, err := store.GetPluginRevision(ctx, "demo", firstPluginRevision); err != nil || !found {
		t.Fatalf("referenced old revision found = %t, err = %v", found, err)
	}
}

func checkPluginStoreRetainsAndSafelyPurgesRevisions(t *testing.T, open contractOpener) {
	t.Parallel()
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	store := provider.Plugins()
	now := time.Now().UTC()
	for _, id := range []string{"active", "old"} {
		if err := store.PutPluginRevision(ctx, PluginRevisionRecord{PluginID: "demo", RevisionID: id, RelativeRoot: "revisions/" + id, CapabilityJSON: `{}`, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	install := PluginInstallRecord{PluginID: "demo", OriginSource: "source", OriginPath: "demo", ActiveRevisionID: "active", Enabled: true, CapabilityJSON: `{}`, DataRelativePath: "data/demo", UpdatedAt: now}
	intent := PluginActivationIntent{IntentID: "install", PluginID: "demo", ToRevisionID: "active", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := store.ActivatePlugin(ctx, intent, install); err != nil {
		t.Fatal(err)
	}
	if can, err := store.CanPurgePluginRevision(ctx, "demo", "active"); err != nil || can {
		t.Fatalf("active can purge = %t, err = %v", can, err)
	}
	if err := store.RetirePluginRevision(ctx, "demo", "old", now); err != nil {
		t.Fatal(err)
	}
	if can, err := store.CanPurgePluginRevision(ctx, "demo", "old"); err != nil || !can {
		t.Fatalf("old can purge = %t, err = %v", can, err)
	}
	if err := store.PurgePluginRevision(ctx, "demo", "old"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetPluginRevision(ctx, "demo", "old"); err != nil || found {
		t.Fatalf("old found = %t, err = %v", found, err)
	}
}

func checkPluginStoreRejectsConflictingRevisionAndTraversal(t *testing.T, open contractOpener) {
	t.Parallel()
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Now().UTC()
	record := PluginRevisionRecord{PluginID: "demo", RevisionID: "rev", RelativeRoot: "revisions/demo/rev", CapabilityJSON: `{}`, CreatedAt: now}
	if err := provider.Plugins().PutPluginRevision(ctx, record); err != nil {
		t.Fatal(err)
	}
	conflict := record
	conflict.CapabilityJSON = `{"skills":1}`
	if err := provider.Plugins().PutPluginRevision(ctx, conflict); err == nil {
		t.Fatal("conflicting immutable revision error = nil")
	}
	record.RevisionID = "escape"
	record.RelativeRoot = "../outside"
	if err := provider.Plugins().PutPluginRevision(ctx, record); err == nil {
		t.Fatal("traversing revision root error = nil")
	}
}

func checkSessionMemoryIngressOutboxPersistsFIFOClaimsAcrossRestart(t *testing.T, open contractOpener) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	provider, err := open(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	first := testIngressRecord(t, "turn-1", now)
	second := testIngressRecord(t, "turn-2", now.Add(time.Second))
	storedFirst, created, err := store.EnqueueSessionMemoryIngress(ctx, first)
	if err != nil || !created || storedFirst.ScopeSequence != 1 {
		t.Fatalf("first EnqueueSessionMemoryIngress() = %#v, created %t, error %v", storedFirst, created, err)
	}
	storedSecond, created, err := store.EnqueueSessionMemoryIngress(ctx, second)
	if err != nil || !created || storedSecond.ScopeSequence != 2 {
		t.Fatalf("second EnqueueSessionMemoryIngress() = %#v, created %t, error %v", storedSecond, created, err)
	}
	replay, created, err := store.EnqueueSessionMemoryIngress(ctx, first)
	if err != nil || created || replay.ScopeSequence != 1 {
		t.Fatalf("replay EnqueueSessionMemoryIngress() = %#v, created %t, error %v", replay, created, err)
	}
	leaseUntil := now.Add(time.Minute)
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, leaseUntil, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ExportID() != first.ExportID() || claimed[0].Attempts != 1 {
		t.Fatalf("first ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	if err := store.MarkSessionMemoryIngressPublished(ctx, first.ExportID(), "worker-1", now); err != nil {
		t.Fatalf("MarkSessionMemoryIngressPublished() error = %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	provider, err = open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	claimed, err = provider.SessionMemoryIngressOutbox().ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(time.Second), now.Add(2*time.Minute), 10)
	if err != nil || len(claimed) != 1 || claimed[0].ExportID() != second.ExportID() || claimed[0].ScopeSequence != 2 {
		t.Fatalf("reopened ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
}

func checkSessionMemoryIngressOutboxReplaysTypedToolEvidenceWithoutDuplication(t *testing.T, open contractOpener) {
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	completedAt := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	turn, err := sessionmemory.NewTerminalTurnWithTools(
		sessionmemory.Scope{Key: "telegram:ingress-tools:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-tools", AgentSessionID: "agent-tools"},
		"turn-tools", completedAt, "question", "answer",
		[]sessionmemory.Message{{ToolName: "calendar.lookup", ToolCallID: "call-1", Text: "2026-08-06"}},
		sessionmemory.TurnTerminalStatusSuccess,
	)
	if err != nil {
		t.Fatalf("NewTerminalTurnWithTools() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, completedAt)
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	store := provider.SessionMemoryIngressOutbox()
	stored, created, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil || !created {
		t.Fatalf("first EnqueueSessionMemoryIngress() = %#v, created %t, error %v", stored, created, err)
	}
	replay, created, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil || created || replay.ScopeSequence != stored.ScopeSequence || replay.Export.Turn == nil || len(replay.Export.Turn.Messages) != 3 {
		t.Fatalf("replay EnqueueSessionMemoryIngress() = %#v, created %t, error %v", replay, created, err)
	}
	tool := replay.Export.Turn.Messages[2]
	if tool.Role != sessionmemory.MessageRoleTool || tool.MessageID != sessionmemory.TurnToolMessageID(turn.ExportID, "calendar.lookup", "call-1") || tool.Text != "2026-08-06" {
		t.Fatalf("replayed tool evidence = %#v", tool)
	}
}

func checkSessionMemoryIngressOutboxRecoversExpiredLeaseAndRejectsForeignSettlement(t *testing.T, open contractOpener) {
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	record := testIngressRecord(t, "turn-1", now)
	stored, _, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil {
		t.Fatalf("EnqueueSessionMemoryIngress() error = %v", err)
	}
	if _, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, now.Add(time.Minute), 1); err != nil {
		t.Fatalf("ClaimSessionMemoryIngress() error = %v", err)
	}
	if err := store.MarkSessionMemoryIngressPublished(ctx, stored.ExportID(), "worker-2", now); err == nil {
		t.Fatal("foreign worker settled ingress lease")
	}
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(time.Minute+time.Second), now.Add(2*time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 || claimed[0].LeaseOwner != "worker-2" {
		t.Fatalf("recovered ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	releasedAt := now.Add(time.Minute + 2*time.Second)
	retryAt := releasedAt.Add(10 * time.Second)
	if err := store.ReleaseSessionMemoryIngress(ctx, stored.ExportID(), "worker-2", "temporary", false, &retryAt, releasedAt); err != nil {
		t.Fatalf("ReleaseSessionMemoryIngress() error = %v", err)
	}
	claimed, err = store.ClaimSessionMemoryIngress(ctx, "worker-3", retryAt.Add(-time.Nanosecond), retryAt.Add(time.Minute), 1)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("early ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	claimed, err = store.ClaimSessionMemoryIngress(ctx, "worker-3", retryAt, retryAt.Add(time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 3 {
		t.Fatalf("retry ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
}

func checkSessionMemoryIngressOutboxReplaysTerminalWithAuditAndStats(t *testing.T, open contractOpener) {
	ctx := context.Background()
	provider, err := open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	stored, _, err := store.EnqueueSessionMemoryIngress(ctx, testIngressRecord(t, "turn-terminal", now))
	if err != nil {
		t.Fatalf("EnqueueSessionMemoryIngress() error = %v", err)
	}
	if _, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, now.Add(time.Minute), 1); err != nil {
		t.Fatalf("ClaimSessionMemoryIngress() error = %v", err)
	}
	if err := store.ReleaseSessionMemoryIngress(ctx, stored.ExportID(), "worker-1", "limit reached", true, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("ReleaseSessionMemoryIngress() error = %v", err)
	}
	stats, err := store.SessionMemoryIngressStats(ctx, now.Add(2*time.Minute))
	if err != nil || stats.PendingCount != 0 || stats.TerminalCount != 1 {
		t.Fatalf("SessionMemoryIngressStats() = %#v, error %v", stats, err)
	}
	if err := store.ReplaySessionMemoryIngress(ctx, stored.ExportID(), "operator-1", "fixed transport", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ReplaySessionMemoryIngress() error = %v", err)
	}
	stats, err = store.SessionMemoryIngressStats(ctx, now.Add(2*time.Minute))
	if err != nil || stats.PendingCount != 1 || stats.TerminalCount != 0 || stats.OldestPendingAge != 2*time.Minute {
		t.Fatalf("SessionMemoryIngressStats() after replay = %#v, error %v", stats, err)
	}
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(2*time.Minute), now.Add(3*time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("replayed ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	concrete := contractDatabase(provider)
	var auditCount int
	if err := concrete.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_memory_ingress_audit WHERE export_id = $1 AND action = 'replay_terminal' AND actor = 'operator-1'`, stored.ExportID()).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("replay audit count = %d, error %v", auditCount, err)
	}
}

func testIngressRecord(t *testing.T, sourceTurnID string, completedAt time.Time) sessionmemorycmd.IngressRecord {
	t.Helper()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:ingress:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "session-1"},
		sourceTurnID, completedAt, "hello", "hi",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn export error = %v", err)
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, completedAt)
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	return record
}
func newContractProvider(t *testing.T, open contractOpener) Provider {
	t.Helper()
	p, err := open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func closeContractProvider(t *testing.T, p Provider) {
	t.Helper()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}
func contractDatabase(p Provider) struct{ db *sql.DB } {
	switch v := p.(type) {
	case *sqliteProvider:
		return struct{ db *sql.DB }{v.db}
	case *postgresProvider:
		return struct{ db *sql.DB }{v.db}
	default:
		panic("unknown contract provider")
	}
}
