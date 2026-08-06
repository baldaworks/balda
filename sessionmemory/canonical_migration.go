package sessionmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const canonicalMigrationSchemaVersion = "session-memory-migration/v1-to-v2"

// CanonicalMigrationConfig supplies the bounded mutation size and independent
// source/atom/operation ranges for one v1 snapshot batch.
type CanonicalMigrationConfig struct {
	MaxMutationRecords   int
	SourceOffset         int
	SourceLimit          int
	AtomOffset           int
	AtomLimit            int
	ScenarioOffset       int
	ScenarioLimit        int
	ProfileOffset        int
	ProfileLimit         int
	OperationOffset      int
	OperationLimit       int
	SkipSourceRecords    bool
	SkipAtomRecords      bool
	SkipScenarioRecords  bool
	SkipProfileRecords   bool
	SkipOperationRecords bool
	LegacyOperations     []OperationOutcome
}

// CanonicalMigrationCheckpoint is the durable cursor for one v1 snapshot.
// Source, atom, and operation batches advance independently so an interrupted
// migration can replay the last committed operation without duplicating
// records.
type CanonicalMigrationCheckpoint struct {
	SchemaVersion       string `json:"schema_version"`
	Scope               Scope  `json:"scope"`
	SnapshotVersion     uint64 `json:"snapshot_version"`
	SourceCount         uint32 `json:"source_count"`
	AtomCount           uint32 `json:"atom_count"`
	ScenarioCount       uint32 `json:"scenario_count"`
	ProfileCount        uint32 `json:"profile_count"`
	OperationCount      uint32 `json:"operation_count"`
	NextSourceOffset    uint32 `json:"next_source_offset"`
	NextAtomOffset      uint32 `json:"next_atom_offset"`
	NextScenarioOffset  uint32 `json:"next_scenario_offset"`
	NextProfileOffset   uint32 `json:"next_profile_offset"`
	NextOperationOffset uint32 `json:"next_operation_offset"`
	Completed           bool   `json:"completed"`
}

const canonicalMigrationCheckpointSchemaVersion = "session-memory-migration-checkpoint/v1"

// Validate verifies a resumable migration cursor belongs to one exact scope
// and cannot move beyond the validated source snapshot.
func (c CanonicalMigrationCheckpoint) Validate() error {
	if c.SchemaVersion != canonicalMigrationCheckpointSchemaVersion {
		return invalidDerived("unsupported canonical migration checkpoint schema")
	}
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if c.NextSourceOffset > c.SourceCount || c.NextAtomOffset > c.AtomCount || c.NextScenarioOffset > c.ScenarioCount || c.NextProfileOffset > c.ProfileCount || c.NextOperationOffset > c.OperationCount {
		return invalidDerived("canonical migration checkpoint cursor exceeds its snapshot")
	}
	if c.Completed && (c.NextSourceOffset != c.SourceCount || c.NextAtomOffset != c.AtomCount || c.NextScenarioOffset != c.ScenarioCount || c.NextProfileOffset != c.ProfileCount || c.NextOperationOffset != c.OperationCount) {
		return invalidDerived("completed canonical migration checkpoint is not at the snapshot end")
	}
	return nil
}

// CanonicalMigrationCheckpointStore persists migration cursors. Implementers
// must make a cursor visible only after the corresponding canonical mutation
// is durable; replaying a cursor's preceding operation is therefore safe.
type CanonicalMigrationCheckpointStore interface {
	LoadCanonicalMigrationCheckpoint(ctx context.Context, scope Scope, snapshotVersion uint64) (CanonicalMigrationCheckpoint, bool, error)
	SaveCanonicalMigrationCheckpoint(ctx context.Context, checkpoint CanonicalMigrationCheckpoint) error
}

// CanonicalMigrationReadiness is the durable cutover gate for one exact
// scope.  It contains no legacy payload; the snapshot version only identifies
// which validated source was migrated.
type CanonicalMigrationReadiness struct {
	SchemaVersion   string    `json:"schema_version"`
	Scope           Scope     `json:"scope"`
	SnapshotVersion uint64    `json:"snapshot_version"`
	ReadyAt         time.Time `json:"ready_at"`
}

const CanonicalMigrationReadinessSchemaVersion = "session-memory-migration-readiness/v1"

func (r CanonicalMigrationReadiness) Validate() error {
	if r.SchemaVersion != CanonicalMigrationReadinessSchemaVersion || r.ReadyAt.IsZero() {
		return invalidDerived("canonical migration readiness is invalid")
	}
	return r.Scope.Validate()
}

// CanonicalMigrationReadinessStore persists the explicit per-scope cutover
// gate so a reopened process never advertises an unproven legacy scope.
type CanonicalMigrationReadinessStore interface {
	LoadCanonicalMigrationReadiness(ctx context.Context, scope Scope) (CanonicalMigrationReadiness, bool, error)
	SaveCanonicalMigrationReadiness(ctx context.Context, readiness CanonicalMigrationReadiness) error
}

// MigrateV1ScopeSnapshot converts one validated v1 exact-scope snapshot into a
// bounded v2 mutation. The operation identity includes the source snapshot
// version, so retrying the same batch is an exact canonical replay; the CAS
// prevents a migration from overwriting concurrent v2 writes. Durable cursor
// checkpoints and composition-root cutover remain outside this portable batch
// transformer.
func MigrateV1ScopeSnapshot(ctx context.Context, store CanonicalStore, snapshot ScopeSnapshot, config CanonicalMigrationConfig) (CanonicalMutationOutcome, error) {
	if ctx == nil || store == nil {
		return CanonicalMutationOutcome{}, PermanentError(CodeStoreFailure, "canonical migration dependencies are required", nil)
	}
	if err := snapshot.Validate(MaxSnapshotItems); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	maxRecords := config.MaxMutationRecords
	if maxRecords <= 0 {
		maxRecords = maxCanonicalMutationRecords
	}
	if maxRecords > maxCanonicalMutationRecords {
		return CanonicalMutationOutcome{}, limitExceeded("canonical migration mutation bound exceeds the store limit")
	}
	sourceStart, sourceEnd, err := migrationRange(len(snapshot.Sources), config.SourceOffset, config.SourceLimit)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	atomStart, atomEnd, err := migrationRange(len(snapshot.Atoms), config.AtomOffset, config.AtomLimit)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	scenarioStart, scenarioEnd, err := migrationRange(len(snapshot.Scenarios), config.ScenarioOffset, config.ScenarioLimit)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	profileStart, profileEnd, err := migrationRange(len(snapshot.Profiles), config.ProfileOffset, config.ProfileLimit)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	nextSourceOffset, nextAtomOffset := sourceStart, atomStart
	nextScenarioOffset, nextProfileOffset := scenarioStart, profileStart
	legacyOperations := make([]OperationOutcome, len(config.LegacyOperations))
	for index, operation := range config.LegacyOperations {
		legacyOperations[index] = cloneOperationOutcome(operation)
	}
	sort.Slice(legacyOperations, func(left, right int) bool {
		if legacyOperations[left].OperationID != legacyOperations[right].OperationID {
			return legacyOperations[left].OperationID < legacyOperations[right].OperationID
		}
		return legacyOperations[left].Stage < legacyOperations[right].Stage
	})
	if err := validateMigrationOperations(snapshot.Scope, legacyOperations); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	operationStart, operationEnd, err := migrationRange(len(legacyOperations), config.OperationOffset, config.OperationLimit)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if config.SkipSourceRecords {
		if sourceStart != len(snapshot.Sources) || sourceEnd != len(snapshot.Sources) {
			return CanonicalMutationOutcome{}, invalidDerived("canonical migration cannot skip an unprocessed source range")
		}
	}
	if config.SkipAtomRecords {
		if config.AtomOffset != 0 || config.AtomLimit != 0 {
			return CanonicalMutationOutcome{}, invalidDerived("canonical migration cannot skip a selected atom range")
		}
		atomStart, atomEnd = 0, 0
	}
	if config.SkipScenarioRecords {
		if config.ScenarioOffset != 0 || config.ScenarioLimit != 0 {
			return CanonicalMutationOutcome{}, invalidDerived("canonical migration cannot skip a selected scenario range")
		}
		scenarioStart, scenarioEnd = 0, 0
	}
	if config.SkipProfileRecords {
		if config.ProfileOffset != 0 || config.ProfileLimit != 0 {
			return CanonicalMutationOutcome{}, invalidDerived("canonical migration cannot skip a selected profile range")
		}
		profileStart, profileEnd = 0, 0
	}
	if config.SkipOperationRecords {
		if config.OperationOffset != 0 || config.OperationLimit != 0 {
			return CanonicalMutationOutcome{}, invalidDerived("canonical migration cannot skip a selected operation range")
		}
		operationStart, operationEnd = 0, 0
	}
	if config.SkipSourceRecords {
		sourceStart, sourceEnd = len(snapshot.Sources), len(snapshot.Sources)
	}
	if config.SkipScenarioRecords {
		scenarioStart, scenarioEnd = len(snapshot.Scenarios), len(snapshot.Scenarios)
	}
	if config.SkipProfileRecords {
		profileStart, profileEnd = len(snapshot.Profiles), len(snapshot.Profiles)
	}
	if config.SkipOperationRecords {
		operationStart, operationEnd = len(legacyOperations), len(legacyOperations)
	}
	if sourceStart == sourceEnd && atomStart == atomEnd && scenarioStart == scenarioEnd && profileStart == profileEnd && operationStart == operationEnd {
		return CanonicalMutationOutcome{}, invalidDerived("canonical migration batch is empty")
	}
	if (atomStart != atomEnd || scenarioStart != scenarioEnd || profileStart != profileEnd) && !config.SkipSourceRecords && (sourceStart != 0 || sourceEnd != len(snapshot.Sources)) {
		return CanonicalMutationOutcome{}, invalidDerived("canonical migration atom batch must include every source or resume after source completion")
	}
	if atomStart != atomEnd {
		if err := validateMigrationAtomRange(snapshot, atomStart, atomEnd); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	if scenarioStart != scenarioEnd {
		if err := validateMigrationScenarioRange(snapshot, scenarioStart, scenarioEnd); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	if profileStart != profileEnd {
		if err := validateMigrationProfileRange(snapshot, profileStart, profileEnd); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	if err := checkContext(ctx); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	state, err := store.LoadScopeState(ctx, snapshot.Scope)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if state.Scope != snapshot.Scope {
		return CanonicalMutationOutcome{}, PermanentError(CodeScopeViolation, "canonical migration scope state does not match snapshot", nil)
	}

	operationID := reconciliationID("migration", canonicalMigrationSchemaVersion, snapshot.Scope.Key, string(snapshot.Scope.Kind), formatUint(snapshot.Version), formatUint(uint64(sourceStart)), formatUint(uint64(sourceEnd)), formatUint(uint64(atomStart)), formatUint(uint64(atomEnd)), formatUint(uint64(scenarioStart)), formatUint(uint64(scenarioEnd)), formatUint(uint64(profileStart)), formatUint(uint64(profileEnd)), formatUint(uint64(operationStart)), formatUint(uint64(operationEnd)), formatBool(config.SkipSourceRecords), formatBool(config.SkipAtomRecords), formatBool(config.SkipScenarioRecords), formatBool(config.SkipProfileRecords), formatBool(config.SkipOperationRecords))
	mutation := CanonicalMutation{
		SchemaVersion:        CanonicalSchemaVersionV1,
		Scope:                snapshot.Scope,
		ExpectedScopeVersion: state.Version,
		Operation: OperationRecord{
			OperationID: operationID,
			Fingerprint: reconciliationID("migration-fingerprint", operationID, snapshot.SchemaVersion, migrationOperationsFingerprint(legacyOperations[operationStart:operationEnd])),
			CommittedAt: migrationCommittedAt(snapshot),
		},
	}
	for _, legacy := range legacyOperations[operationStart:operationEnd] {
		mutation.ImportedOperations = append(mutation.ImportedOperations, CanonicalImportedOperation{
			SchemaVersion: CanonicalImportedOperationSchemaVersion,
			Outcome:       cloneOperationOutcome(legacy),
		})
	}

	sources, messages, payloads, sourceIDs, sourceMessages, err := migrationSources(ctx, snapshot, sourceStart, sourceEnd, !config.SkipSourceRecords)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	mutation.Sources = sources
	mutation.Messages = messages
	mutation.Payloads = append(mutation.Payloads, payloads...)

	legacyRevisions := make(map[RevisionRef]string, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms {
		legacyRevisions[RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}] = migrationRevisionID(snapshot.Scope, atom.Meta.RevisionID)
	}
	for _, scenario := range snapshot.Scenarios {
		legacyRevisions[RevisionRef{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID}] = migrationRevisionID(snapshot.Scope, scenario.Meta.RevisionID)
	}
	for _, profile := range snapshot.Profiles {
		legacyRevisions[RevisionRef{ItemID: profile.Meta.ItemID, RevisionID: profile.Meta.RevisionID}] = migrationRevisionID(snapshot.Scope, profile.Meta.RevisionID)
	}
	items := make(map[string]MemoryItem, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	knownItems := make(map[string]MemoryKind, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms[:atomStart] {
		if err := rememberMigrationItem(items, knownItems, snapshot.Scope, atom.Meta, migrationMemoryKind(atom.Category)); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	for _, scenario := range snapshot.Scenarios[:scenarioStart] {
		if err := rememberMigrationItem(items, knownItems, snapshot.Scope, scenario.Meta, MemoryKindState); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	for _, profile := range snapshot.Profiles[:profileStart] {
		if err := rememberMigrationItem(items, knownItems, snapshot.Scope, profile.Meta, MemoryKindState); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	activeHeads := make(map[string]MemoryRevision)
	appendRevision := func(meta RevisionMeta, kind MemoryKind, category *AtomCategory, topicKey, title, text string) error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		evidence, err := migrationEvidence(snapshot.Scope, meta.Provenance.RawSources, sourceIDs, sourceMessages)
		if err != nil {
			return err
		}
		itemID := migrationItemID(snapshot.Scope, meta.ItemID)
		if err := rememberMigrationItem(items, knownItems, snapshot.Scope, meta, kind); err != nil {
			return err
		}
		compat := CanonicalCompatibilityPayload{SchemaVersion: CanonicalCompatibilitySchemaVersion, Kind: migrationDerivedKind(kind, category, topicKey, title), Category: category, TopicKey: topicKey, Title: title, Text: text, LegacyItemID: meta.ItemID, LegacyRevisionID: meta.RevisionID, LegacyOperationID: meta.OperationID, LegacyParents: append([]RevisionRef(nil), meta.Provenance.ParentRevisions...)}
		if meta.Supersedes != nil {
			copyOf := *meta.Supersedes
			compat.Supersedes = &copyOf
		}
		compatBytes, err := json.Marshal(compat)
		if err != nil {
			return PermanentError(CodeInvalidDerived, "encode v1 migration compatibility payload", err)
		}
		revisionID := migrationRevisionID(snapshot.Scope, meta.RevisionID)
		revisionPayload, revisionRef, err := canonicalMigrationPayload(canonicalPayloadID("migration-revision", revisionID), compatBytes)
		if err != nil {
			return err
		}
		parents := make([]string, 0, len(meta.Provenance.ParentRevisions)+1)
		for _, parent := range meta.Provenance.ParentRevisions {
			mapped, ok := legacyRevisions[parent]
			if !ok {
				return invalidDerived("v1 migration references a revision outside the snapshot")
			}
			parents = appendUniqueCanonicalID(parents, mapped)
		}
		if meta.Supersedes != nil {
			mapped, ok := legacyRevisions[RevisionRef{ItemID: meta.Supersedes.ItemID, RevisionID: meta.Supersedes.RevisionID}]
			if !ok {
				return invalidDerived("v1 migration supersedes a revision outside the snapshot")
			}
			parents = appendUniqueCanonicalID(parents, mapped)
		}
		revision := MemoryRevision{SchemaVersion: MemorySchemaVersionV2, RevisionID: revisionID, ItemID: itemID, Revision: meta.Revision, Parents: parents, Temporal: Temporal{ObservedAt: meta.CreatedAt}, Evidence: evidence, Sensitivity: SensitivityStandard, Retention: RetentionClassStandard, Payload: revisionRef}
		if err := revision.Validate(); err != nil {
			return err
		}
		mutation.Revisions = append(mutation.Revisions, revision)
		mutation.Lifecycle = append(mutation.Lifecycle, LifecycleEvent{EventID: reconciliationID("legacy-lifecycle", revisionID, string(meta.State)), RevisionID: revisionID, Type: migrationLifecycleType(meta.State), OccurredAt: meta.CreatedAt})
		mutation.Payloads = append(mutation.Payloads, revisionPayload)
		mutation.Operation.Outcome = append(mutation.Operation.Outcome, revisionID)
		if meta.State == RevisionStateActive {
			if current, ok := activeHeads[itemID]; !ok || current.Revision < revision.Revision || (current.Revision == revision.Revision && revision.RevisionID < current.RevisionID) {
				activeHeads[itemID] = revision
			}
		}
		return nil
	}
	for _, atom := range snapshot.Atoms[atomStart:atomEnd] {
		category := atom.Category
		if err := appendRevision(atom.Meta, migrationMemoryKind(atom.Category), &category, "", "", atom.Text); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	for _, scenario := range snapshot.Scenarios[scenarioStart:scenarioEnd] {
		if err := appendRevision(scenario.Meta, MemoryKindState, nil, scenario.TopicKey, scenario.Title, scenario.Summary); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	for _, profile := range snapshot.Profiles[profileStart:profileEnd] {
		if err := appendRevision(profile.Meta, MemoryKindState, nil, "", "", profile.Summary); err != nil {
			return CanonicalMutationOutcome{}, err
		}
	}
	for _, item := range items {
		mutation.Items = append(mutation.Items, item)
	}
	for itemID, revision := range activeHeads {
		mutation.Heads = append(mutation.Heads, ItemHead{ItemID: itemID, RevisionID: revision.RevisionID})
	}
	sort.Slice(mutation.Sources, func(left, right int) bool { return mutation.Sources[left].SourceID < mutation.Sources[right].SourceID })
	sort.Slice(mutation.Messages, func(left, right int) bool {
		return mutation.Messages[left].MessageID < mutation.Messages[right].MessageID
	})
	sort.Slice(mutation.Items, func(left, right int) bool { return mutation.Items[left].ItemID < mutation.Items[right].ItemID })
	sort.Slice(mutation.Revisions, func(left, right int) bool {
		return mutation.Revisions[left].RevisionID < mutation.Revisions[right].RevisionID
	})
	sort.Slice(mutation.Lifecycle, func(left, right int) bool {
		return mutation.Lifecycle[left].EventID < mutation.Lifecycle[right].EventID
	})
	sort.Slice(mutation.Heads, func(left, right int) bool { return mutation.Heads[left].ItemID < mutation.Heads[right].ItemID })
	sort.Slice(mutation.Payloads, func(left, right int) bool { return mutation.Payloads[left].Ref.ID < mutation.Payloads[right].Ref.ID })
	if len(mutation.Revisions) > 0 {
		mutation.Operation.Outcome = mutation.Operation.Outcome[:0]
		for _, revision := range mutation.Revisions {
			mutation.Operation.Outcome = append(mutation.Operation.Outcome, revision.RevisionID)
		}
	}
	if recordCount := len(mutation.ImportedOperations) + len(mutation.Sources) + len(mutation.Messages) + len(mutation.Items) + len(mutation.Revisions) + len(mutation.Lifecycle) + len(mutation.Heads) + len(mutation.Payloads); recordCount > maxRecords {
		return CanonicalMutationOutcome{}, limitExceeded("v1 migration batch exceeds the configured mutation bound")
	}
	if err := mutation.Validate(); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	outcome, err := store.ApplyCanonicalMutation(ctx, mutation)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	checkpoint := CanonicalMigrationCheckpoint{
		SchemaVersion:       canonicalMigrationCheckpointSchemaVersion,
		Scope:               snapshot.Scope,
		SnapshotVersion:     snapshot.Version,
		SourceCount:         uint32(len(snapshot.Sources)),
		AtomCount:           uint32(len(snapshot.Atoms)),
		ScenarioCount:       uint32(len(snapshot.Scenarios)),
		ProfileCount:        uint32(len(snapshot.Profiles)),
		OperationCount:      uint32(len(legacyOperations)),
		NextSourceOffset:    uint32(nextMigrationOffset(config.SkipSourceRecords, nextSourceOffset, sourceEnd)),
		NextAtomOffset:      uint32(nextMigrationOffset(config.SkipAtomRecords, nextAtomOffset, atomEnd)),
		NextScenarioOffset:  uint32(nextMigrationOffset(config.SkipScenarioRecords, nextScenarioOffset, scenarioEnd)),
		NextProfileOffset:   uint32(nextMigrationOffset(config.SkipProfileRecords, nextProfileOffset, profileEnd)),
		NextOperationOffset: uint32(nextMigrationOffset(config.SkipOperationRecords, config.OperationOffset, operationEnd)),
		Completed:           nextMigrationOffset(config.SkipSourceRecords, nextSourceOffset, sourceEnd) == len(snapshot.Sources) && nextMigrationOffset(config.SkipAtomRecords, nextAtomOffset, atomEnd) == len(snapshot.Atoms) && nextMigrationOffset(config.SkipScenarioRecords, nextScenarioOffset, scenarioEnd) == len(snapshot.Scenarios) && nextMigrationOffset(config.SkipProfileRecords, nextProfileOffset, profileEnd) == len(snapshot.Profiles) && nextMigrationOffset(config.SkipOperationRecords, config.OperationOffset, operationEnd) == len(legacyOperations),
	}
	if checkpointStore, ok := store.(CanonicalMigrationCheckpointStore); ok {
		if err := checkpointStore.SaveCanonicalMigrationCheckpoint(ctx, checkpoint); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

type migrationSourceMessage struct {
	SourceID string
	Message  Message
}

func migrationSources(ctx context.Context, snapshot ScopeSnapshot, sourceStart, sourceEnd int, emitRecords bool) ([]SourceRecordV2, []MessageRecord, []CanonicalPayload, map[string]string, map[string]migrationSourceMessage, error) {
	sources := make([]SourceRecordV2, 0, sourceEnd-sourceStart)
	messages := make([]MessageRecord, 0)
	payloads := make([]CanonicalPayload, 0)
	sourceIDs := make(map[string]string, len(snapshot.Sources))
	sourceMessages := make(map[string]migrationSourceMessage)
	for _, source := range snapshot.Sources {
		if err := checkContext(ctx); err != nil {
			return nil, nil, nil, nil, nil, err
		}
		sourceID := reconciliationID("legacy-source", snapshot.Scope.Key, source.Ref.ExportID)
		sourceIDs[source.Ref.ExportID] = sourceID
		if source.Turn != nil {
			for _, message := range normalizedTurnMessages(*source.Turn) {
				if _, exists := sourceMessages[source.Ref.ExportID]; !exists && message.Role == MessageRoleUser {
					sourceMessages[source.Ref.ExportID] = migrationSourceMessage{SourceID: sourceID, Message: message}
				}
			}
		}
	}
	if !emitRecords {
		return sources, messages, payloads, sourceIDs, sourceMessages, nil
	}
	for _, source := range snapshot.Sources[sourceStart:sourceEnd] {
		if err := checkContext(ctx); err != nil {
			return nil, nil, nil, nil, nil, err
		}
		sourceID := sourceIDs[source.Ref.ExportID]
		normalizedSource := source
		if source.Turn != nil {
			normalizedTurn := cloneTurn(*source.Turn)
			normalizedTurn.Messages = normalizedTurnMessages(*source.Turn)
			normalizedSource.Turn = &normalizedTurn
		}
		plaintext, err := json.Marshal(normalizedSource)
		if err != nil {
			return nil, nil, nil, nil, nil, PermanentError(CodeInvalidDerived, "encode v1 migration source", err)
		}
		if source.Turn != nil {
			for _, message := range normalizedTurnMessages(*source.Turn) {
				messagePayload, messageRef, payloadErr := canonicalMigrationPayload(canonicalPayloadID("migration-message", message.MessageID), []byte(message.Text))
				if payloadErr != nil {
					return nil, nil, nil, nil, nil, payloadErr
				}
				messages = append(messages, MessageRecord{MessageID: message.MessageID, SourceID: sourceID, Role: message.Role, Payload: messageRef})
				payloads = append(payloads, messagePayload)
			}
		}
		sourcePayload, sourceRef, err := canonicalMigrationPayload(canonicalPayloadID("migration-source", sourceID), plaintext)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		sources = append(sources, SourceRecordV2{SourceID: sourceID, Scope: snapshot.Scope, Sensitivity: SensitivityStandard, Retention: RetentionClassStandard, Payload: sourceRef})
		payloads = append(payloads, sourcePayload)
	}
	return sources, messages, payloads, sourceIDs, sourceMessages, nil
}

func normalizedTurnMessages(turn Turn) []Message {
	messages := make([]Message, len(turn.Messages))
	copy(messages, turn.Messages)
	for index := range messages {
		if messages[index].MessageID != "" {
			continue
		}
		if messages[index].Role == MessageRoleTool {
			messages[index].MessageID = TurnToolMessageID(turn.ExportID, messages[index].ToolName, messages[index].ToolCallID)
		} else {
			messages[index].MessageID = TurnMessageID(turn.ExportID, messages[index].Role)
		}
	}
	return messages
}

func migrationEvidence(scope Scope, refs []SourceRef, sourceIDs map[string]string, sourceMessages map[string]migrationSourceMessage) ([]EvidenceRef, error) {
	if len(refs) == 0 {
		return nil, invalidDerived("v1 migration revision has no raw source provenance")
	}
	evidence := make([]EvidenceRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, source := range refs {
		if source.Scope != scope {
			return nil, PermanentError(CodeScopeViolation, "v1 migration source scope does not match snapshot", nil)
		}
		sourceID, ok := sourceIDs[source.ExportID]
		if !ok {
			return nil, invalidDerived("v1 migration source is outside the snapshot")
		}
		message, ok := sourceMessages[source.ExportID]
		if !ok {
			return nil, PermanentError(CodeForgotten, "v1 migration cannot ground a forgotten source revision", nil)
		}
		if _, exists := seen[source.ExportID]; exists {
			continue
		}
		seen[source.ExportID] = struct{}{}
		ref, err := NewEvidenceRef(sourceID, message.Message.MessageID, MessageRoleUser, message.Message.Text, 0, uint32(len(message.Message.Text)), AssertionModeUser)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, ref)
	}
	return evidence, nil
}

func canonicalMigrationPayload(payloadID string, plaintext []byte) (CanonicalPayload, PayloadRef, error) {
	if len(plaintext) == 0 {
		return CanonicalPayload{}, PayloadRef{}, invalidDerived("v1 migration payload is empty")
	}
	digest := sha256.Sum256(plaintext)
	ref := PayloadRef{ID: payloadID, Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(plaintext))}
	payload := CanonicalPayload{Ref: ref, Data: append([]byte(nil), plaintext...)}
	if err := payload.Validate(); err != nil {
		return CanonicalPayload{}, PayloadRef{}, err
	}
	return payload, ref, nil
}

func migrationItemID(scope Scope, oldItemID string) string {
	return reconciliationID("legacy-item", scope.Key, oldItemID)
}

func migrationRevisionID(scope Scope, oldRevisionID string) string {
	return reconciliationID("legacy-revision", scope.Key, oldRevisionID)
}

func migrationLifecycleType(state RevisionState) LifecycleEventType {
	switch state {
	case RevisionStateActive:
		return LifecycleEventActivate
	case RevisionStateSuperseded:
		return LifecycleEventSupersede
	default:
		return LifecycleEventInvalidate
	}
}

func migrationCommittedAt(snapshot ScopeSnapshot) time.Time {
	latest := time.Time{}
	for _, source := range snapshot.Sources {
		if source.Turn != nil && source.Turn.CompletedAt.After(latest) {
			latest = source.Turn.CompletedAt
		}
	}
	for _, atom := range snapshot.Atoms {
		if atom.Meta.CreatedAt.After(latest) {
			latest = atom.Meta.CreatedAt
		}
	}
	for _, scenario := range snapshot.Scenarios {
		if scenario.Meta.CreatedAt.After(latest) {
			latest = scenario.Meta.CreatedAt
		}
	}
	for _, profile := range snapshot.Profiles {
		if profile.Meta.CreatedAt.After(latest) {
			latest = profile.Meta.CreatedAt
		}
	}
	if latest.IsZero() {
		// Operation-only batches have no source timestamp. Use a stable,
		// non-zero sentinel required by the canonical operation contract.
		return time.Unix(0, 0).UTC()
	}
	return latest
}

func appendUniqueCanonicalID(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func migrationRange(total, offset, limit int) (int, int, error) {
	if offset < 0 || offset > total {
		return 0, 0, limitExceeded("canonical migration cursor is outside the snapshot")
	}
	if limit < 0 {
		return 0, 0, limitExceeded("canonical migration batch size cannot be negative")
	}
	end := total
	if limit > 0 && limit < total-offset {
		end = offset + limit
	}
	return offset, end, nil
}

func nextMigrationOffset(skipped bool, cursor, end int) int {
	if skipped {
		return cursor
	}
	return end
}

func rememberMigrationItem(items map[string]MemoryItem, known map[string]MemoryKind, scope Scope, meta RevisionMeta, kind MemoryKind) error {
	itemID := migrationItemID(scope, meta.ItemID)
	if existing, ok := known[itemID]; ok && existing != kind {
		return invalidDerived("v1 migration maps one item to multiple memory kinds")
	}
	known[itemID] = kind
	if existing, ok := items[itemID]; ok {
		if existing.Kind != kind || existing.Scope != scope {
			return invalidDerived("v1 migration maps one item to multiple memory kinds")
		}
		return nil
	}
	item := MemoryItem{ItemID: itemID, Scope: scope, Kind: kind}
	if kind == MemoryKindState {
		item.MemoryKey = MemoryKey(reconciliationID("legacy-key", scope.Key, meta.ItemID))
	}
	items[itemID] = item
	return nil
}

func migrationMemoryKind(category AtomCategory) MemoryKind {
	if category == AtomCategoryEvent {
		return MemoryKindEvent
	}
	return MemoryKindState
}

func migrationDerivedKind(kind MemoryKind, category *AtomCategory, topicKey, title string) DerivedKind {
	if category != nil {
		return DerivedKindAtom
	}
	if topicKey != "" || title != "" {
		return DerivedKindScenario
	}
	return DerivedKindProfile
}

type migrationRevisionRecord struct {
	ref        RevisionRef
	parents    []RevisionRef
	supersedes *RevisionRef
}

func migrationRevisionRecords(snapshot ScopeSnapshot) []migrationRevisionRecord {
	records := make([]migrationRevisionRecord, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms {
		records = append(records, migrationRevisionRecord{ref: RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}, parents: atom.Meta.Provenance.ParentRevisions, supersedes: atom.Meta.Supersedes})
	}
	for _, scenario := range snapshot.Scenarios {
		records = append(records, migrationRevisionRecord{ref: RevisionRef{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID}, parents: scenario.Meta.Provenance.ParentRevisions, supersedes: scenario.Meta.Supersedes})
	}
	for _, profile := range snapshot.Profiles {
		records = append(records, migrationRevisionRecord{ref: RevisionRef{ItemID: profile.Meta.ItemID, RevisionID: profile.Meta.RevisionID}, parents: profile.Meta.Provenance.ParentRevisions, supersedes: profile.Meta.Supersedes})
	}
	return records
}

func validateMigrationRevisionRange(snapshot ScopeSnapshot, absoluteStart, absoluteEnd int) error {
	records := migrationRevisionRecords(snapshot)
	if absoluteStart < 0 || absoluteEnd < absoluteStart || absoluteEnd > len(records) {
		return limitExceeded("v1 migration revision range is outside the snapshot")
	}
	indices := make(map[RevisionRef]int, len(records))
	for index, record := range records {
		indices[record.ref] = index
	}
	for _, record := range records[absoluteStart:absoluteEnd] {
		for _, parent := range record.parents {
			index, ok := indices[parent]
			if !ok {
				return invalidDerived("v1 migration references a revision outside the snapshot")
			}
			if index >= absoluteEnd {
				return invalidDerived("v1 migration batch references a revision from a later batch")
			}
		}
		if record.supersedes != nil {
			index, ok := indices[*record.supersedes]
			if !ok {
				return invalidDerived("v1 migration supersedes a revision outside the snapshot")
			}
			if index >= absoluteEnd {
				return invalidDerived("v1 migration batch supersedes a revision from a later batch")
			}
		}
	}
	return nil
}

func validateMigrationAtomRange(snapshot ScopeSnapshot, start, end int) error {
	return validateMigrationRevisionRange(snapshot, start, end)
}

func validateMigrationScenarioRange(snapshot ScopeSnapshot, start, end int) error {
	base := len(snapshot.Atoms)
	return validateMigrationRevisionRange(snapshot, base+start, base+end)
}

func validateMigrationProfileRange(snapshot ScopeSnapshot, start, end int) error {
	base := len(snapshot.Atoms) + len(snapshot.Scenarios)
	return validateMigrationRevisionRange(snapshot, base+start, base+end)
}

func validateMigrationOperations(scope Scope, operations []OperationOutcome) error {
	seen := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		if err := operation.Validate(); err != nil {
			return err
		}
		if operation.Scope != scope {
			return PermanentError(CodeScopeViolation, "v1 migration operation scope does not match snapshot", nil)
		}
		if _, exists := seen[operation.OperationID]; exists {
			return invalidDerived("v1 migration contains duplicate operation outcomes")
		}
		seen[operation.OperationID] = struct{}{}
	}
	return nil
}

func migrationOperationsFingerprint(operations []OperationOutcome) string {
	encoded, err := json.Marshal(operations)
	if err != nil {
		return "invalid"
	}
	return reconciliationID("migration-operations", string(encoded))
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789"
	var encoded [20]byte
	index := len(encoded)
	for value > 0 {
		index--
		encoded[index] = digits[value%10]
		value /= 10
	}
	return string(encoded[index:])
}

func formatBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
