package sessionmemory

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

const canonicalMigrationSchemaVersion = "session-memory-migration/v1-to-v2"

// CanonicalMigrationConfig supplies the application-owned payload sealer and
// the bounded mutation size for one v1 snapshot batch.
type CanonicalMigrationConfig struct {
	Sealer             CanonicalPayloadSealer
	MaxMutationRecords int
	SourceOffset       int
	SourceLimit        int
	AtomOffset         int
	AtomLimit          int
	SkipSourceRecords  bool
	SkipAtomRecords    bool
}

// CanonicalMigrationCheckpoint is the durable cursor for one v1 snapshot.
// Source and atom batches advance independently so an interrupted migration
// can replay the last committed operation without duplicating records.
type CanonicalMigrationCheckpoint struct {
	SchemaVersion    string `json:"schema_version"`
	Scope            Scope  `json:"scope"`
	SnapshotVersion  uint64 `json:"snapshot_version"`
	SourceCount      uint32 `json:"source_count"`
	AtomCount        uint32 `json:"atom_count"`
	NextSourceOffset uint32 `json:"next_source_offset"`
	NextAtomOffset   uint32 `json:"next_atom_offset"`
	Completed        bool   `json:"completed"`
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
	if c.NextSourceOffset > c.SourceCount || c.NextAtomOffset > c.AtomCount {
		return invalidDerived("canonical migration checkpoint cursor exceeds its snapshot")
	}
	if c.Completed && (c.NextSourceOffset != c.SourceCount || c.NextAtomOffset != c.AtomCount) {
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

// MigrateV1ScopeSnapshot converts one validated v1 exact-scope snapshot into a
// bounded v2 mutation. The operation identity includes the source snapshot
// version, so retrying the same batch is an exact canonical replay; the CAS
// prevents a migration from overwriting concurrent v2 writes. Durable cursor
// checkpoints and composition-root cutover remain outside this portable batch
// transformer.
func MigrateV1ScopeSnapshot(ctx context.Context, store CanonicalStore, snapshot ScopeSnapshot, config CanonicalMigrationConfig) (CanonicalMutationOutcome, error) {
	if ctx == nil || store == nil || config.Sealer == nil {
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
	if config.SkipSourceRecords {
		sourceStart, sourceEnd = len(snapshot.Sources), len(snapshot.Sources)
	}
	if sourceStart == sourceEnd && atomStart == atomEnd {
		return CanonicalMutationOutcome{}, invalidDerived("canonical migration batch is empty")
	}
	if atomStart != atomEnd && !config.SkipSourceRecords && (sourceStart != 0 || sourceEnd != len(snapshot.Sources)) {
		return CanonicalMutationOutcome{}, invalidDerived("canonical migration atom batch must include every source or resume after source completion")
	}
	if atomStart != atomEnd {
		if err := validateMigrationAtomRange(snapshot.Atoms, atomStart, atomEnd); err != nil {
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

	operationID := reconciliationID("migration", canonicalMigrationSchemaVersion, snapshot.Scope.Key, string(snapshot.Scope.Kind), formatUint(snapshot.Version), formatUint(uint64(sourceStart)), formatUint(uint64(sourceEnd)), formatUint(uint64(atomStart)), formatUint(uint64(atomEnd)), formatBool(config.SkipSourceRecords), formatBool(config.SkipAtomRecords))
	mutation := CanonicalMutation{
		SchemaVersion:        CanonicalSchemaVersionV1,
		Scope:                snapshot.Scope,
		ExpectedScopeVersion: state.Version,
		Operation: OperationRecord{
			OperationID: operationID,
			Fingerprint: reconciliationID("migration-fingerprint", operationID, snapshot.SchemaVersion),
			CommittedAt: migrationCommittedAt(snapshot),
		},
	}

	sources, messages, payloads, sourceIDs, sourceMessages, err := migrationSources(ctx, snapshot, config.Sealer, sourceStart, sourceEnd, !config.SkipSourceRecords)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	mutation.Sources = sources
	mutation.Messages = messages
	mutation.Payloads = append(mutation.Payloads, payloads...)

	legacyRevisions := make(map[RevisionRef]string, len(snapshot.Atoms))
	for _, atom := range snapshot.Atoms {
		legacyRevisions[RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}] = migrationRevisionID(snapshot.Scope, atom.Meta.RevisionID)
	}
	items := make(map[string]MemoryItem, len(snapshot.Atoms))
	activeHeads := make(map[string]MemoryRevision)
	for _, atom := range snapshot.Atoms[atomStart:atomEnd] {
		if err := checkContext(ctx); err != nil {
			return CanonicalMutationOutcome{}, err
		}
		evidence, err := migrationEvidence(snapshot.Scope, atom.Meta.Provenance.RawSources, sourceIDs, sourceMessages)
		if err != nil {
			return CanonicalMutationOutcome{}, err
		}
		kind := MemoryKindState
		if atom.Category == AtomCategoryEvent {
			kind = MemoryKindEvent
		}
		itemID := migrationItemID(snapshot.Scope, atom.Meta.ItemID)
		item := MemoryItem{ItemID: itemID, Scope: snapshot.Scope, Kind: kind}
		if kind == MemoryKindState {
			item.MemoryKey = MemoryKey(reconciliationID("legacy-key", snapshot.Scope.Key, atom.Meta.ItemID))
		}
		if existing, ok := items[itemID]; ok && existing.Kind != item.Kind {
			return CanonicalMutationOutcome{}, invalidDerived("v1 migration maps one item to multiple memory kinds")
		}
		items[itemID] = item

		parents := make([]string, 0, len(atom.Meta.Provenance.ParentRevisions)+1)
		for _, parent := range atom.Meta.Provenance.ParentRevisions {
			mapped, ok := legacyRevisions[parent]
			if !ok {
				return CanonicalMutationOutcome{}, invalidDerived("v1 migration references a revision outside the snapshot")
			}
			parents = appendUniqueCanonicalID(parents, mapped)
		}
		if atom.Meta.Supersedes != nil {
			mapped, ok := legacyRevisions[RevisionRef{ItemID: atom.Meta.Supersedes.ItemID, RevisionID: atom.Meta.Supersedes.RevisionID}]
			if !ok {
				return CanonicalMutationOutcome{}, invalidDerived("v1 migration supersedes a revision outside the snapshot")
			}
			parents = appendUniqueCanonicalID(parents, mapped)
		}
		revisionID := migrationRevisionID(snapshot.Scope, atom.Meta.RevisionID)
		revisionPayload, revisionRef, err := sealCanonicalMigrationPayload(ctx, config.Sealer, canonicalPayloadID("migration-revision", revisionID), []byte(atom.Text))
		if err != nil {
			return CanonicalMutationOutcome{}, err
		}
		revision := MemoryRevision{
			SchemaVersion: MemorySchemaVersionV2,
			RevisionID:    revisionID,
			ItemID:        itemID,
			Revision:      atom.Meta.Revision,
			Parents:       parents,
			Temporal:      Temporal{ObservedAt: atom.Meta.CreatedAt},
			Evidence:      evidence,
			Sensitivity:   SensitivityStandard,
			Retention:     RetentionClassStandard,
			Payload:       revisionRef,
		}
		if err := revision.Validate(); err != nil {
			return CanonicalMutationOutcome{}, err
		}
		mutation.Revisions = append(mutation.Revisions, revision)
		mutation.Lifecycle = append(mutation.Lifecycle, LifecycleEvent{
			EventID:    reconciliationID("legacy-lifecycle", revisionID, string(atom.Meta.State)),
			RevisionID: revisionID,
			Type:       migrationLifecycleType(atom.Meta.State),
			OccurredAt: atom.Meta.CreatedAt,
		})
		mutation.Payloads = append(mutation.Payloads, revisionPayload)
		mutation.Operation.Outcome = append(mutation.Operation.Outcome, revisionID)
		if atom.Meta.State == RevisionStateActive {
			if current, ok := activeHeads[itemID]; !ok || current.Revision < revision.Revision || (current.Revision == revision.Revision && revision.RevisionID < current.RevisionID) {
				activeHeads[itemID] = revision
			}
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
	if recordCount := len(mutation.Sources) + len(mutation.Messages) + len(mutation.Items) + len(mutation.Revisions) + len(mutation.Lifecycle) + len(mutation.Heads) + len(mutation.Payloads); recordCount > maxRecords {
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
		SchemaVersion:    canonicalMigrationCheckpointSchemaVersion,
		Scope:            snapshot.Scope,
		SnapshotVersion:  snapshot.Version,
		SourceCount:      uint32(len(snapshot.Sources)),
		AtomCount:        uint32(len(snapshot.Atoms)),
		NextSourceOffset: uint32(sourceEnd),
		NextAtomOffset:   uint32(atomEnd),
		Completed:        sourceEnd == len(snapshot.Sources) && atomEnd == len(snapshot.Atoms),
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

func migrationSources(ctx context.Context, snapshot ScopeSnapshot, sealer CanonicalPayloadSealer, sourceStart, sourceEnd int, emitRecords bool) ([]SourceRecordV2, []MessageRecord, []CanonicalPayload, map[string]string, map[string]migrationSourceMessage, error) {
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
				messagePayload, messageRef, sealErr := sealCanonicalMigrationPayload(ctx, sealer, canonicalPayloadID("migration-message", message.MessageID), []byte(message.Text))
				if sealErr != nil {
					return nil, nil, nil, nil, nil, sealErr
				}
				messages = append(messages, MessageRecord{MessageID: message.MessageID, SourceID: sourceID, Role: message.Role, Payload: messageRef})
				payloads = append(payloads, messagePayload)
			}
		}
		sourcePayload, sourceRef, err := sealCanonicalMigrationPayload(ctx, sealer, canonicalPayloadID("migration-source", sourceID), plaintext)
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

func sealCanonicalMigrationPayload(ctx context.Context, sealer CanonicalPayloadSealer, payloadID string, plaintext []byte) (CanonicalPayload, PayloadRef, error) {
	if len(plaintext) == 0 {
		return CanonicalPayload{}, PayloadRef{}, invalidDerived("v1 migration payload is empty")
	}
	payload, err := sealer.SealCanonicalPayload(ctx, payloadID, plaintext)
	if err != nil {
		return CanonicalPayload{}, PayloadRef{}, err
	}
	if payload.Ref.ID != payloadID {
		return CanonicalPayload{}, PayloadRef{}, invalidDerived("v1 migration sealer changed payload identity")
	}
	if err := payload.Validate(); err != nil {
		return CanonicalPayload{}, PayloadRef{}, err
	}
	return payload, payload.Ref, nil
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

func validateMigrationAtomRange(atoms []Atom, start, end int) error {
	indices := make(map[RevisionRef]int, len(atoms))
	for index, atom := range atoms {
		indices[RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}] = index
	}
	for _, atom := range atoms[start:end] {
		for _, parent := range atom.Meta.Provenance.ParentRevisions {
			index, ok := indices[parent]
			if !ok {
				return invalidDerived("v1 migration references a revision outside the snapshot")
			}
			if index >= end {
				return invalidDerived("v1 migration batch references a revision from a later batch")
			}
		}
		if atom.Meta.Supersedes != nil {
			ref := *atom.Meta.Supersedes
			index, ok := indices[ref]
			if !ok {
				return invalidDerived("v1 migration supersedes a revision outside the snapshot")
			}
			if index >= end {
				return invalidDerived("v1 migration batch supersedes a revision from a later batch")
			}
		}
	}
	return nil
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
