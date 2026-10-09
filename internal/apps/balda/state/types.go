package state

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	adksession "google.golang.org/adk/v2/session"
)

// ErrScheduledJobSourceConflict means an ID is already owned by another source.
var ErrScheduledJobSourceConflict = errors.New("scheduled job source conflict")

const (
	// NamespaceApp stores balda app internal state (for example owner auth).
	NamespaceApp = "balda.app"
	// NamespaceSessionMCP stores balda.state MCP key-value data.
	NamespaceSessionMCP = "balda.session_mcp"

	// SessionStatusActive marks a session that can be lazily restored.
	SessionStatusActive = "active"

	// ChannelTypeTelegram is the current balda channel type backed by Telegram.
	ChannelTypeTelegram = string(deliverycmd.ChannelTypeTelegram)

	// ChannelTypeZulip is the balda channel type backed by Zulip.
	ChannelTypeZulip = string(deliverycmd.ChannelTypeZulip)

	// ChannelTypeSlackAgent is the balda channel type backed by the Slack AI Agents integration.
	ChannelTypeSlackAgent = string(deliverycmd.ChannelTypeSlackAgent)

	// ScheduledJobStatusActive means the job is eligible for scheduler dispatch.
	ScheduledJobStatusActive = "active"
	// ScheduledJobStatusPaused means the job is persisted but not dispatched.
	ScheduledJobStatusPaused = "paused"
	// ScheduledJobSourceConfig means host configuration owns the definition.
	ScheduledJobSourceConfig = "config"
	// ScheduledJobSourceManaged means Backoffice owns the definition.
	ScheduledJobSourceManaged = "managed"
	// ScheduledJobSourceInternal means the runtime owns a one-shot timer.
	ScheduledJobSourceInternal = "internal"

	// JobStatusCreated means a job record exists but has not been queued.
	JobStatusCreated = "created"
	// JobStatusQueued means job work is queued for actor execution.
	JobStatusQueued = "queued"
	// JobStatusRunning means a job actor is actively coordinating work.
	JobStatusRunning = "running"
	// JobStatusWaitingForAgent means job execution waits on an agent role.
	JobStatusWaitingForAgent = "waiting_for_agent"
	// JobStatusWaitingForUser means job execution is blocked on user input.
	JobStatusWaitingForUser = "waiting_for_user"
	// JobStatusValidating means a reviewer/validator is checking the work.
	JobStatusValidating = "validating"
	// JobStatusCompleted means the job finished successfully.
	JobStatusCompleted = "completed"
	// JobStatusFailed means the job exhausted its retry/iteration budget.
	JobStatusFailed = "failed"
	// JobStatusCanceled means the job was canceled before completion.
	JobStatusCanceled = "canceled"
	// JobStatusDeadLettered means the actor runtime deadlettered the job.
	JobStatusDeadLettered = "deadlettered"

	// DeliveryStatusPending means a delivery side effect is reserved but not confirmed.
	DeliveryStatusPending = "pending"
	// DeliveryStatusSending means a delivery side effect attempt is in progress
	// or its outcome is ambiguous after process failure.
	DeliveryStatusSending = "sending"
	// DeliveryStatusSent means a delivery side effect was successfully sent.
	DeliveryStatusSent = "sent"
	// DeliveryStatusFailed means the latest delivery attempt failed.
	DeliveryStatusFailed = "failed"

	// AgentStepStatusRunning means an agent step has been reserved but no result is stored.
	AgentStepStatusRunning = "running"
	// AgentStepStatusSucceeded means an agent step result is stored and can be replayed.
	AgentStepStatusSucceeded = "succeeded"
	// AgentStepStatusFailed means an agent step error result is stored and can be replayed.
	AgentStepStatusFailed = "failed"
)

const PrivateRunKindWebhook = "webhook"

// Provider exposes balda state capabilities behind a backend-agnostic interface.
// This allows swapping SQLite with another provider later.
type Provider interface {
	AppKV() KVStore
	RuntimeSessions() adksession.Service
	SessionMCPKV() KVStore
	Sessions() SessionStore
	ScheduledJobs() ScheduledJobStore
	ScheduleManagement() ScheduleManagementStore
	ScheduleRuns() ScheduleRunStore
	Aliases() AliasStore
	WebhookAdmissions() WebhookAdmissionStore
	WebhookRoutes() WebhookRouteStore
	Questions() QuestionStore
	// SessionMemoryIngressOutbox returns producer-local exports awaiting
	// JetStream PubAck. It is distinct from canonical memory delivery state.
	SessionMemoryIngressOutbox() SessionMemoryIngressOutboxStore
	Jobs() JobStore
	PollingOffsetStore() PollingOffsetStore
	Collaborators() CollaboratorStore
	Plugins() PluginStore
	MCP() MCPStore
	Users() usercmd.Store
	Close() error
}

// WebhookAdmissionStore freezes inbound job input and delivery selection.
// It persists admission, but never executes or schedules actor work.
type WebhookAdmissionStore interface {
	Get(ctx context.Context, routeName, dedupeKey string) (webhookcmd.Admission, bool, error)
	GetByJobID(ctx context.Context, jobID string) (webhookcmd.Admission, bool, error)
	Create(ctx context.Context, candidate webhookcmd.Admission) (webhookcmd.Admission, bool, error)
	RecordReceipt(ctx context.Context, routeName, dedupeKey string, receipt webhookcmd.Receipt) (webhookcmd.Admission, error)
	ListHistory(ctx context.Context, routeName string, beforeAt time.Time, beforeJobID string, limit int) ([]WebhookHistoryRecord, error)
	GetHistory(ctx context.Context, routeName, jobID string) (WebhookHistoryRecord, bool, error)
}

const (
	// WebhookHistorySourceExternal identifies an admitted external HTTP request.
	WebhookHistorySourceExternal = webhookcmd.SourceExternal
	// WebhookHistorySourceTest identifies an administrator Test POST.
	WebhookHistorySourceTest = webhookcmd.SourceTest
)

// WebhookHistoryRecord joins one durable admission to its optional job and final delivery.
// RawBody is nil for admissions created before raw input was stored.
type WebhookHistoryRecord struct {
	RouteName       string
	JobID           string
	Source          string
	RawBody         *string
	ReportTo        *deliverycmd.Locator
	CreatedAt       time.Time
	JobStatus       string
	Output          string
	DeliveryStatus  string
	DeliveryPayload string
}

const (
	PluginActivationIntentPending  = "pending"
	PluginActivationIntentComplete = "complete"
)

// PluginRevisionRecord identifies immutable validated plugin package bytes.
type PluginRevisionRecord struct {
	PluginID       string
	RevisionID     string
	Version        string
	Description    string
	RelativeRoot   string
	CapabilityJSON string
	CreatedAt      time.Time
	RetiredAt      time.Time
}

// PluginInstallRecord is the durable active state for one logical plugin.
type PluginInstallRecord struct {
	PluginID          string
	OriginMarketplace string
	OriginSource      string
	OriginPath        string
	ActiveRevisionID  string
	Enabled           bool
	Version           string
	Description       string
	CapabilityJSON    string
	DataRelativePath  string
	UpdatedAt         time.Time
}

// PluginActivationIntent makes an active-revision switch recoverable.
type PluginActivationIntent struct {
	IntentID       string
	PluginID       string
	FromRevisionID string
	ToRevisionID   string
	Operation      string
	State          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PluginStore persists plugin revisions, active installs, and activation intents.
type PluginStore interface {
	PutPluginRevision(ctx context.Context, record PluginRevisionRecord) error
	GetPluginRevision(ctx context.Context, pluginID, revisionID string) (PluginRevisionRecord, bool, error)
	ListPluginRevisions(ctx context.Context, pluginID string) ([]PluginRevisionRecord, error)
	GetPluginInstall(ctx context.Context, pluginID string) (PluginInstallRecord, bool, error)
	ListPluginInstalls(ctx context.Context) ([]PluginInstallRecord, error)
	ActivatePlugin(ctx context.Context, intent PluginActivationIntent, install PluginInstallRecord) error
	AdoptPluginOrigin(ctx context.Context, intent PluginActivationIntent, install PluginInstallRecord) error
	DeactivatePlugin(ctx context.Context, intent PluginActivationIntent) error
	SetPluginEnabled(ctx context.Context, pluginID string, enabled bool, updatedAt time.Time) error
	CompletePluginActivation(ctx context.Context, intentID string, updatedAt time.Time) error
	ListIncompletePluginActivations(ctx context.Context) ([]PluginActivationIntent, error)
	RetirePluginRevision(ctx context.Context, pluginID, revisionID string, retiredAt time.Time) error
	CanPurgePluginRevision(ctx context.Context, pluginID, revisionID string) (bool, error)
	PurgePluginRevision(ctx context.Context, pluginID, revisionID string) error
}

// PollingOffsetStore persists bot polling offsets across restarts.
type PollingOffsetStore interface {
	Load(ctx context.Context) (int, error)
	Save(ctx context.Context, offset int) error
}

// SessionMemoryIngressOutboxStore persists producer-local session-memory
// exports before publication. Records are claimed in exact-scope FIFO order.
type SessionMemoryIngressOutboxStore interface {
	EnqueueSessionMemoryIngress(ctx context.Context, record sessionmemorycmd.IngressRecord) (sessionmemorycmd.IngressRecord, bool, error)
	ClaimSessionMemoryIngress(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]sessionmemorycmd.IngressRecord, error)
	MarkSessionMemoryIngressPublished(ctx context.Context, exportID, owner string, publishedAt time.Time) error
	ReleaseSessionMemoryIngress(ctx context.Context, exportID, owner, reason string, terminal bool, nextAttemptAt *time.Time, updatedAt time.Time) error
	ReplaySessionMemoryIngress(ctx context.Context, exportID, actor, reason string, replayedAt time.Time) error
	SessionMemoryIngressStats(ctx context.Context, now time.Time) (sessionmemorycmd.IngressOutboxStats, error)
}

// KVStore stores string and JSON key/value records.
type KVStore interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Clear(ctx context.Context) error
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	// ConsumeJSON atomically reads a JSON value and deletes it when shouldConsume returns true.
	ConsumeJSON(ctx context.Context, key string, shouldConsume func(value any) (bool, error)) (value any, consumed bool, err error)
	SetJSON(ctx context.Context, key string, value any) error
	SetWithTTL(ctx context.Context, key string, value any, ttl time.Duration) error
	MergeJSON(ctx context.Context, key string, fields map[string]any) (merged map[string]any, err error)
}

// CollaboratorStore persists authorized collaborators.
type CollaboratorStore interface {
	AddCollaborator(ctx context.Context, c authcmd.Collaborator) error
	RemoveCollaborator(ctx context.Context, userID string) error
	GetCollaborator(ctx context.Context, userID string) (*authcmd.Collaborator, bool, error)
	ListCollaborators(ctx context.Context) ([]authcmd.Collaborator, error)
}

// SessionRecord persists balda session metadata for lazy restore.
type SessionRecord struct {
	SessionID    string
	UserID       string
	ChannelType  string
	AddressKey   string
	AddressJSON  string
	AgentName    string
	WorkspaceDir string
	BranchName   string
	// RuntimeSnapshotID pins provider capabilities for the lifetime of the session.
	RuntimeSnapshotID string
	Status            string
}

// SessionStore persists balda session metadata.
type SessionStore interface {
	Upsert(ctx context.Context, record SessionRecord) error
	GetByAddress(ctx context.Context, channelType, addressKey string) (SessionRecord, bool, error)
	GetBySessionID(ctx context.Context, sessionID string) (SessionRecord, bool, error)
	DeleteBySessionID(ctx context.Context, sessionID string) error
	List(ctx context.Context) ([]SessionRecord, error)
}

// ScheduledJobRecord persists recurring job metadata and optional report targeting.
type ScheduledJobRecord struct {
	JobID               string
	Source              string
	Enabled             bool
	Deleted             bool
	DefinitionVersion   uint64
	TargetKind          string
	TargetKey           string
	ReportToTargetKind  string
	ReportToTargetKey   string
	SessionID           string
	ChannelType         string
	AddressKey          string
	AddressJSON         string
	ReportToEnabled     bool
	ReportToSessionID   string
	ReportToChannelType string
	ReportToAddressKey  string
	ReportToAddressJSON string
	Content             string
	ScheduleSpec        string
	Timezone            string
	Status              string
	MaxRetries          int
	RetryCount          int
	LastDispatchKey     string
	NextRunAt           time.Time
	LastRunAt           time.Time
	LastError           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ScheduledJobStore persists scheduler jobs bound to canonical locators.
type ScheduledJobStore interface {
	Upsert(ctx context.Context, record ScheduledJobRecord) error
	UpdateRuntime(ctx context.Context, update ScheduledJobRuntimeUpdate) (bool, error)
	GetByID(ctx context.Context, jobID string) (ScheduledJobRecord, bool, error)
	List(ctx context.Context) ([]ScheduledJobRecord, error)
	ListByAddress(ctx context.Context, channelType, addressKey string) ([]ScheduledJobRecord, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]ScheduledJobRecord, error)
	Delete(ctx context.Context, jobID string) error
}

// ScheduledJobRuntimeUpdate changes only dispatch state when the selected definition and slot still match.
type ScheduledJobRuntimeUpdate struct {
	JobID                   string
	DefinitionVersion       uint64
	ExpectedNextRunAt       time.Time
	ExpectedLastDispatchKey string
	Status                  string
	RetryCount              int
	LastDispatchKey         string
	NextRunAt               time.Time
	LastRunAt               time.Time
	LastError               string
}

// ScheduleRunRecord persists one exact manual or cron dispatch intent.
type ScheduleRunRecord struct {
	RunID             string
	ScheduleID        string
	Trigger           string
	TriggerKey        string
	DefinitionVersion uint64
	Version           uint64
	RequestedAt       time.Time
	DueAt             time.Time
	DispatchState     string
	Attempts          int
	NextAttemptAt     time.Time
	SafeFailureCode   string
	DispatchedAt      time.Time
	ExecutionJobID    string
	PayloadJSON       string
	ReportLocatorRef  string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const (
	// ScheduleRunTriggerCron marks a due cron slot.
	ScheduleRunTriggerCron = "cron"
	// ScheduleRunTriggerManual marks an administrator-requested run.
	ScheduleRunTriggerManual = "manual"
	// ScheduleRunPending means an intent is awaiting durable dispatch.
	ScheduleRunPending = "pending"
	// ScheduleRunRetrying means a dispatch retry is due later.
	ScheduleRunRetrying = "retrying"
	// ScheduleRunPublishing means a worker claimed publication until its lease expires.
	ScheduleRunPublishing = "publishing"
	// ScheduleRunDispatched means the actor command was durably published.
	ScheduleRunDispatched = "dispatched"
	// ScheduleRunFailed means pre-publication dispatch exhausted its retries.
	ScheduleRunFailed = "failed"
	// ScheduleRunCanceled means a stale unclaimed intent was canceled.
	ScheduleRunCanceled = "canceled"
)

// ScheduleRunStore persists run intents and lists history independently of definitions.
type ScheduleRunStore interface {
	Create(ctx context.Context, record ScheduleRunRecord) (bool, error)
	CreateCron(ctx context.Context, record ScheduleRunRecord, expectedNextRunAt time.Time) (bool, error)
	ClaimCron(ctx context.Context, record ScheduleRunRecord, now, leaseUntil time.Time) (bool, error)
	Update(ctx context.Context, record ScheduleRunRecord, expectedVersion uint64) (bool, error)
	GetByID(ctx context.Context, runID string) (ScheduleRunRecord, bool, error)
	GetByTriggerKey(ctx context.Context, scheduleID, triggerKey string) (ScheduleRunRecord, bool, error)
	ListBySchedule(ctx context.Context, scheduleID string, beforeAt time.Time, beforeID string, limit int) ([]ScheduleRunRecord, error)
	ListPending(ctx context.Context, now time.Time, limit int) ([]ScheduleRunRecord, error)
	ListUnclosedDispatched(ctx context.Context, afterAt time.Time, afterID string, limit int) ([]ScheduleRunRecord, error)
	MarkClosed(ctx context.Context, runID string, at time.Time) error
}

type QuestionRecord struct {
	QuestionID        string
	SessionID         string
	ChannelKind       string
	AddressKey        string
	AddressJSON       string
	Prompt            string
	Status            string
	InteractionJSON   string
	ResumeJSON        string
	RequestJSON       string
	AnswerJSON        string
	FailureJSON       string
	Provider          string
	ConversationKey   string
	ProviderMessageID string
	ReplyHandle       string
	ControlHandle     string
	ExpiresAt         time.Time
	AnsweredAt        time.Time
	FailedAt          time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type QuestionStore interface {
	CreatePendingQuestion(ctx context.Context, record QuestionRecord) error
	BindQuestionDeliveryRef(ctx context.Context, questionID string, ref questioncmd.DeliveryRef) error
	GetQuestionByID(ctx context.Context, questionID string) (QuestionRecord, bool, error)
	GetPendingQuestionByReplyRef(ctx context.Context, provider, conversationKey, replyToMessageID string) (QuestionRecord, bool, error)
	MarkQuestionAnswered(ctx context.Context, questionID string, answer questioncmd.Answer) (QuestionRecord, bool, error)
	MarkQuestionTimedOut(ctx context.Context, questionID string, timedOutAt time.Time) (QuestionRecord, bool, error)
	MarkQuestionFailed(ctx context.Context, questionID string, failure questioncmd.Failure) (QuestionRecord, bool, error)
}

// JobRecord persists one assignable work item.
type JobRecord struct {
	ID                 string
	SessionID          string
	ParentJobID        string
	Title              string
	Objective          string
	Status             string
	OwnerActor         string
	AssignedActor      string
	Priority           int
	CreatedBy          string
	Result             string
	Error              string
	PrivateRunKind     string
	PrivateRunClosedAt string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	StartedAt          time.Time
	CompletedAt        time.Time
	CanceledAt         time.Time
}

// JobEventRecord persists an append-only job event.
type JobEventRecord struct {
	ID        string
	JobID     string
	EventType string
	Actor     string
	MessageID string
	Payload   string
	CreatedAt time.Time
}

// JobEventOutboxRecord persists one job event awaiting publication.
type JobEventOutboxRecord struct {
	ID          string
	JobID       string
	Subject     string
	Envelope    string
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	PublishedAt time.Time
}

// DeliveryRecord persists idempotency state for external delivery side effects.
type DeliveryRecord struct {
	ID                string
	DeliveryKey       string
	JobID             string
	SessionID         string
	Channel           string
	AddressKey        string
	Kind              string
	Payload           string
	PayloadHash       string
	Status            string
	ProviderMessageID string
	SentAt            time.Time
	Error             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// AgentStepRecord persists idempotency state for one logical agent step.
type AgentStepRecord struct {
	ID          string
	StepKey     string
	JobID       string
	AgentName   string
	Role        string
	Iteration   int
	PayloadHash string
	Status      string
	Result      string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
}

// JobLifecycleStore persists job state transitions.
type JobLifecycleStore interface {
	CreateJob(ctx context.Context, record JobRecord) (bool, error)
	CreateJobWithEvent(ctx context.Context, record JobRecord, event JobEventOutboxRecord) (bool, error)
	GetJob(ctx context.Context, jobID string) (JobRecord, bool, error)
	ListActiveJobsBySession(ctx context.Context, sessionID string) ([]JobRecord, error)
	RebindScheduledJobSession(ctx context.Context, jobID, oldSessionID, newSessionID, assignedActor string) (bool, error)
	UpdateJobStatus(ctx context.Context, jobID string, status string, reason string) error
	UpdateJobStatusWithEvent(ctx context.Context, jobID string, status string, reason string, event JobEventOutboxRecord) error
	SetJobResult(ctx context.Context, jobID string, result string, status string, reason string) error
	SetJobResultWithEvent(ctx context.Context, jobID string, result string, status string, reason string, event JobEventOutboxRecord) error
}

// PrivateRunCleanupStore scans and marks private execution sessions for cleanup.
type PrivateRunCleanupStore interface {
	ListUnclosedTerminalWebhookJobs(ctx context.Context, after time.Time, afterID string, before time.Time, limit int) ([]JobRecord, error)
	MarkWebhookRunClosed(ctx context.Context, jobID string, closedAt time.Time) error
}

// WebhookTurnClaimStore guards the first provider invocation for a webhook job.
type WebhookTurnClaimStore interface {
	ClaimWebhookTurn(ctx context.Context, jobID string) (bool, error)
}

// ScheduledOutputStore records one private scheduled run's provider output.
type ScheduledOutputStore interface {
	RecordScheduledOutput(ctx context.Context, jobID, output string) error
	RecordPrivateOutput(ctx context.Context, jobID, output string, failed bool) error
}

// JobEventStore persists projected job history.
type JobEventStore interface {
	AppendJobEvent(ctx context.Context, record JobEventRecord) error
	ListJobEvents(ctx context.Context, jobID string) ([]JobEventRecord, error)
}

// JobEventOutboxStore persists job events until publication succeeds.
type JobEventOutboxStore interface {
	EnqueueJobEvent(ctx context.Context, event JobEventOutboxRecord) error
	ListPendingJobEvents(ctx context.Context, limit int) ([]JobEventOutboxRecord, error)
	MarkJobEventPublished(ctx context.Context, eventID string) error
	MarkJobEventPublishFailed(ctx context.Context, eventID string, reason string) error
}

// DeliveryStore persists idempotent external deliveries.
type DeliveryStore interface {
	ReserveDelivery(ctx context.Context, record DeliveryRecord) (DeliveryRecord, bool, error)
	MarkDeliverySending(ctx context.Context, deliveryKey string) error
	MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error
	MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error
	SentFinalDelivery(ctx context.Context, jobID string) (string, bool, error)
	FinalDelivery(ctx context.Context, jobID string) (DeliveryRecord, bool, error)
}

// AgentStepStore persists idempotent agent workflow steps.
type AgentStepStore interface {
	ReserveAgentStep(ctx context.Context, record AgentStepRecord) (AgentStepRecord, bool, error)
	CompleteAgentStep(ctx context.Context, stepKey string, resultJSON string) error
	FailAgentStep(ctx context.Context, stepKey string, resultJSON string, reason string) error
}

// JobStore is the complete SQLite capability set exposed by the state provider.
type JobStore interface {
	JobLifecycleStore
	WebhookTurnClaimStore
	PrivateRunCleanupStore
	ScheduledOutputStore
	JobEventStore
	JobEventOutboxStore
	DeliveryStore
	AgentStepStore
}
