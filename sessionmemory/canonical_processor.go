package sessionmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// CanonicalTurnProcessor owns the v2 turn-processing dependencies. Its
// implementation is introduced separately so composition can cut over from
// the legacy SQLite provider without leaking Badger types into the core.
type CanonicalTurnProcessor struct {
	store     CanonicalStore
	extractor CanonicalSemanticExtractor
	policy    PolicyRegistry
}

// NewCanonicalTurnProcessor validates the mandatory cutover dependencies.
func NewCanonicalTurnProcessor(store CanonicalStore, extractor CanonicalSemanticExtractor, policy PolicyRegistry) (*CanonicalTurnProcessor, error) {
	if store == nil || extractor == nil {
		return nil, PermanentError(CodeStoreFailure, "canonical processor dependencies are required", nil)
	}
	if !isCanonicalID(policy.Version) {
		return nil, invalidDerived("canonical reconciliation policy version is required")
	}
	return &CanonicalTurnProcessor{store: store, extractor: extractor, policy: policy}, nil
}

// ProcessTurn extracts bounded semantics from one terminal turn and commits
// the source, messages, and derived revisions as one canonical mutation. The
// model supplies only semantic fields and evidence claims; all identities,
// payload references, revision numbers, and scope state are derived here.
func (p *CanonicalTurnProcessor) ProcessTurn(ctx context.Context, turn Turn, derivation DerivationRef) (CanonicalMutationOutcome, error) {
	if p == nil || p.store == nil || p.extractor == nil {
		return CanonicalMutationOutcome{}, PermanentError(CodeDisabled, "canonical turn processor is unavailable", nil)
	}
	if ctx == nil {
		return CanonicalMutationOutcome{}, invalidDerived("canonical turn processing context is required")
	}
	if err := turn.Validate(); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	turn = cloneTurn(turn)
	expectedSourceID := TurnSourceID(turn.ExportID)
	if turn.SourceID != "" && turn.SourceID != expectedSourceID {
		return CanonicalMutationOutcome{}, PermanentError(CodePermanent, "canonical turn source identity does not match export", nil)
	}
	turn.SourceID = expectedSourceID
	for index := range turn.Messages {
		if turn.Messages[index].MessageID != "" {
			continue
		}
		if turn.Messages[index].Role == MessageRoleTool {
			turn.Messages[index].MessageID = TurnToolMessageID(turn.ExportID, turn.Messages[index].ToolName, turn.Messages[index].ToolCallID)
		} else {
			turn.Messages[index].MessageID = TurnMessageID(turn.ExportID, turn.Messages[index].Role)
		}
	}
	if err := derivation.Validate(); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if turnTextExceeds(turn, MaxDerivedTurnTextBytes) {
		return CanonicalMutationOutcome{}, limitExceeded("canonical turn text exceeds the derived processing limit")
	}
	if err := checkContext(ctx); err != nil {
		return CanonicalMutationOutcome{}, err
	}

	scopeState, err := p.store.LoadScopeState(ctx, turn.Scope)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if scopeState.Scope != turn.Scope {
		return CanonicalMutationOutcome{}, PermanentError(CodeScopeViolation, "canonical scope state does not match turn", nil)
	}

	active, err := p.store.ScanActiveMemory(ctx, ActiveMemoryScanRequest{
		Scope: turn.Scope,
		Limit: maxCanonicalReadRecords,
	})
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	activeView := make([]ActiveMemoryItem, len(active))
	for i, memory := range active {
		if err := validateActiveCanonicalMemory(turn.Scope, memory); err != nil {
			return CanonicalMutationOutcome{}, err
		}
		activeView[i] = ActiveMemoryItem{Item: memory.Item, Evidence: append([]EvidenceRef(nil), memory.Evidence...)}
	}

	candidates, err := p.extractor.ExtractCanonicalSemantics(ctx, CanonicalExtractionRequest{
		SchemaVersion: CanonicalSchemaVersionV1,
		Derivation:    derivation,
		Turn:          cloneTurn(turn),
	})
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if len(candidates) > MaxCandidateCount {
		return CanonicalMutationOutcome{}, limitExceeded("canonical semantic candidate count exceeds the limit")
	}

	mutation, err := p.buildCanonicalMutation(ctx, turn, derivation, scopeState, active, activeView, candidates)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	outcome, err := p.store.ApplyCanonicalMutation(ctx, mutation)
	if err != nil {
		return CanonicalMutationOutcome{}, err
	}
	if err := outcome.Validate(); err != nil {
		return CanonicalMutationOutcome{}, err
	}
	return outcome, nil
}

func (p *CanonicalTurnProcessor) buildCanonicalMutation(ctx context.Context, turn Turn, derivation DerivationRef, scopeState ScopeState, active []ActiveCanonicalMemory, activeView []ActiveMemoryItem, candidates []SemanticCandidate) (CanonicalMutation, error) {
	sourceID := turn.SourceID
	if !isCanonicalID(sourceID) {
		return CanonicalMutation{}, invalidDerived("canonical turn source identity is invalid")
	}

	sourcePayload, err := json.Marshal(canonicalTurnSourcePayload{
		ExportID:       turn.ExportID,
		SessionID:      turn.Session.SessionID,
		AgentSessionID: turn.Session.AgentSessionID,
		SourceTurnID:   turn.SourceTurnID,
		CompletedAt:    turn.CompletedAt,
		TerminalStatus: normalizedTerminalStatus(turn),
	})
	if err != nil {
		return CanonicalMutation{}, PermanentError(CodeInvalidDerived, "encode canonical source payload", err)
	}

	operationID, err := CanonicalSemanticOperationID(turn.ExportID, derivation)
	if err != nil {
		return CanonicalMutation{}, err
	}
	mutation := CanonicalMutation{
		SchemaVersion:        CanonicalSchemaVersionV1,
		Scope:                turn.Scope,
		ExpectedScopeVersion: scopeState.Version,
		Operation: OperationRecord{
			OperationID: operationID,
			Fingerprint: mustCanonicalFingerprint(turn.ExportID, derivation),
			CommittedAt: turn.CompletedAt,
		},
	}

	sourcePayloadRecord, sourceRef, err := p.payload(canonicalPayloadID("source", sourceID), sourcePayload)
	if err != nil {
		return CanonicalMutation{}, err
	}
	mutation.Sources = []SourceRecordV2{{SourceID: sourceID, Scope: turn.Scope, Sensitivity: SensitivityStandard, Retention: RetentionClassStandard, Payload: sourceRef}}
	mutation.Payloads = append(mutation.Payloads, sourcePayloadRecord)

	messageByID := make(map[string]Message, len(turn.Messages))
	for _, message := range turn.Messages {
		if message.MessageID == "" {
			return CanonicalMutation{}, invalidDerived("canonical turn message identity is required")
		}
		if _, exists := messageByID[message.MessageID]; exists {
			return CanonicalMutation{}, invalidDerived("canonical turn message identity is duplicated")
		}
		messageByID[message.MessageID] = message
		payload, payloadRef, payloadErr := p.payload(canonicalPayloadID("message", message.MessageID), []byte(message.Text))
		if payloadErr != nil {
			return CanonicalMutation{}, payloadErr
		}
		mutation.Messages = append(mutation.Messages, MessageRecord{MessageID: message.MessageID, SourceID: sourceID, Role: message.Role, Payload: payloadRef})
		mutation.Payloads = append(mutation.Payloads, payload)
	}

	seenItems := make(map[string]struct{}, len(candidates))
	seenRevisions := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		canonicalCandidate, err := canonicalizeCandidate(sourceID, messageByID, candidate)
		if err != nil {
			return CanonicalMutation{}, err
		}
		reconciliation, err := p.policy.ReconcileActive(turn.Scope, canonicalCandidate, activeView)
		if err != nil {
			return CanonicalMutation{}, err
		}
		if _, exists := seenItems[reconciliation.Item.ItemID]; exists {
			return CanonicalMutation{}, invalidDerived("canonical semantic candidates resolve to one item more than once")
		}
		if _, exists := seenRevisions[reconciliation.RevisionID]; exists {
			return CanonicalMutation{}, invalidDerived("canonical semantic candidates contain duplicate revisions")
		}
		seenItems[reconciliation.Item.ItemID] = struct{}{}
		seenRevisions[reconciliation.RevisionID] = struct{}{}

		parentRevision, revisionNumber, err := activeRevision(active, reconciliation.Supersedes)
		if err != nil {
			return CanonicalMutation{}, err
		}
		revisionPayload, revisionRef, err := p.payload(canonicalPayloadID("revision", reconciliation.RevisionID), []byte(canonicalCandidate.Memory.Statement))
		if err != nil {
			return CanonicalMutation{}, err
		}
		revision := MemoryRevision{
			SchemaVersion: MemorySchemaVersionV2,
			RevisionID:    reconciliation.RevisionID,
			ItemID:        reconciliation.Item.ItemID,
			Revision:      revisionNumber,
			Parents:       nil,
			Temporal:      canonicalCandidate.Memory.Temporal,
			Evidence:      append([]EvidenceRef(nil), reconciliation.Provenance...),
			Sensitivity:   canonicalCandidate.Memory.Sensitivity,
			Retention:     canonicalCandidate.Memory.Retention,
			Payload:       revisionRef,
		}
		if parentRevision != nil {
			revision.Parents = []string{parentRevision.RevisionID}
		}
		if reconciliation.Action == ReconciliationActionCreate {
			mutation.Items = append(mutation.Items, reconciliation.Item)
		}
		mutation.Revisions = append(mutation.Revisions, revision)
		mutation.Lifecycle = append(mutation.Lifecycle, reconciliation.Lifecycle)
		mutation.Heads = append(mutation.Heads, ItemHead{ItemID: reconciliation.Item.ItemID, RevisionID: reconciliation.RevisionID})
		mutation.Payloads = append(mutation.Payloads, revisionPayload)
		mutation.Operation.Outcome = append(mutation.Operation.Outcome, reconciliation.RevisionID)
	}

	if err := mutation.Validate(); err != nil {
		return CanonicalMutation{}, err
	}
	return mutation, nil
}

type canonicalTurnSourcePayload struct {
	ExportID       string             `json:"export_id"`
	SessionID      string             `json:"session_id"`
	AgentSessionID string             `json:"agent_session_id"`
	SourceTurnID   string             `json:"source_turn_id"`
	CompletedAt    time.Time          `json:"completed_at"`
	TerminalStatus TurnTerminalStatus `json:"terminal_status"`
}

func (p *CanonicalTurnProcessor) payload(payloadID string, plaintext []byte) (CanonicalPayload, PayloadRef, error) {
	if len(plaintext) == 0 {
		return CanonicalPayload{}, PayloadRef{}, invalidDerived("canonical payload is empty")
	}
	digest := sha256.Sum256(plaintext)
	ref := PayloadRef{ID: payloadID, Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(plaintext))}
	payload := CanonicalPayload{Ref: ref, Data: append([]byte(nil), plaintext...)}
	if err := payload.Validate(); err != nil {
		return CanonicalPayload{}, PayloadRef{}, err
	}
	return payload, ref, nil
}

func canonicalizeCandidate(sourceID string, messages map[string]Message, candidate SemanticCandidate) (SemanticCandidate, error) {
	if err := candidate.Memory.Validate(); err != nil {
		return SemanticCandidate{}, err
	}
	if len(candidate.Memory.Evidence) == 0 {
		return SemanticCandidate{}, invalidDerived("canonical candidate evidence is required")
	}
	canonical := candidate
	canonical.Memory.Evidence = make([]EvidenceRef, 0, len(candidate.Memory.Evidence))
	for _, claimed := range candidate.Memory.Evidence {
		message, ok := messages[claimed.MessageID]
		if !ok || claimed.SourceID != sourceID || claimed.Role != message.Role {
			return SemanticCandidate{}, PermanentError(CodeScopeViolation, "canonical evidence is not grounded in the captured turn", nil)
		}
		ref, err := NewEvidenceRef(sourceID, message.MessageID, message.Role, message.Text, claimed.StartByte, claimed.EndByte, claimed.AssertionMode)
		if err != nil {
			return SemanticCandidate{}, err
		}
		if claimed.TextDigest != "" && claimed.TextDigest != ref.TextDigest {
			return SemanticCandidate{}, invalidDerived("canonical evidence text digest does not match captured message")
		}
		canonical.Memory.Evidence = append(canonical.Memory.Evidence, ref)
	}
	return canonical, nil
}

func validateActiveCanonicalMemory(scope Scope, active ActiveCanonicalMemory) error {
	if err := active.Item.Validate(); err != nil {
		return PermanentError(CodeStoreFailure, "stored active canonical item is invalid", err)
	}
	if active.Item.Scope != scope || !isCanonicalID(active.RevisionID) || active.Revision == 0 {
		return PermanentError(CodeStoreFailure, "stored active canonical revision is invalid", nil)
	}
	if err := validateEvidenceRefs(active.Evidence); err != nil {
		return PermanentError(CodeStoreFailure, "stored active canonical evidence is invalid", err)
	}
	return nil
}

func activeRevision(active []ActiveCanonicalMemory, item *MemoryItem) (*MemoryRevision, uint64, error) {
	if item == nil {
		return nil, 1, nil
	}
	for _, existing := range active {
		if existing.Item.ItemID == item.ItemID {
			return &MemoryRevision{RevisionID: existing.RevisionID, Revision: existing.Revision}, existing.Revision + 1, nil
		}
	}
	return nil, 0, PermanentError(CodeStoreFailure, "reconciler selected an unobserved active item", nil)
}

func canonicalPayloadID(kind, identity string) string {
	return reconciliationID("payload", kind, identity)
}

func mustCanonicalFingerprint(exportID string, derivation DerivationRef) string {
	return reconciliationID("fingerprint", exportID, derivation.Pipeline, derivation.Policy, derivation.Prompt, derivation.Model)
}

func normalizedTerminalStatus(turn Turn) TurnTerminalStatus {
	if strings.TrimSpace(string(turn.TerminalStatus)) == "" {
		return TurnTerminalStatusSuccess
	}
	return turn.TerminalStatus
}
