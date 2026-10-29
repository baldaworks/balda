package state

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baldaworks/balda/internal/apps/balda/providers"
)

const (
	maxRuntimeOperationIDBytes         = 256
	maxRuntimeSessionIDBytes           = 512
	maxRuntimeKindBytes                = 64
	maxRuntimeArchiveIDBytes           = 512
	maxRuntimeSnapshotIDBytes          = 512
	maxRuntimeProviderBindingJSONBytes = 64 * 1024
	maxEffectiveModelBytes             = 512
	maxRuntimeOutcomeBytes             = 64
)

func prepareSessionRuntimeMetadata(record *SessionRecord) (int64, error) {
	if record.RuntimeGeneration > math.MaxInt64 {
		return 0, fmt.Errorf("runtime_generation exceeds storage range")
	}
	if record.EffectiveModel != "" && !structuralPublicValue(record.EffectiveModel, maxEffectiveModelBytes) {
		return 0, fmt.Errorf("effective_model is invalid")
	}
	if err := prepareOptionalRequestedModel(&record.RequestedModel); err != nil {
		return 0, err
	}
	if record.RuntimeOutcome != "" && !structuralPublicValue(record.RuntimeOutcome, maxRuntimeOutcomeBytes) {
		return 0, fmt.Errorf("runtime_outcome is invalid")
	}
	return int64(record.RuntimeGeneration), nil
}

func prepareOptionalRequestedModel(model **string) error {
	if *model == nil {
		return nil
	}
	normalized := strings.TrimSpace(**model)
	if !structuralPublicValue(normalized, maxEffectiveModelBytes) {
		return fmt.Errorf("requested_model is invalid")
	}
	*model = &normalized
	return nil
}

func prepareRuntimeOperationRecord(record *RuntimeOperationRecord) error {
	if !structuralPublicValue(record.OperationID, maxRuntimeOperationIDBytes) {
		return fmt.Errorf("operation_id is invalid")
	}
	if !structuralPublicValue(record.SessionID, maxRuntimeSessionIDBytes) {
		return fmt.Errorf("session_id is invalid")
	}
	if len(record.InputHash) != 64 {
		return fmt.Errorf("input_hash is invalid")
	}
	if _, err := hex.DecodeString(record.InputHash); err != nil || strings.ToLower(record.InputHash) != record.InputHash {
		return fmt.Errorf("input_hash is invalid")
	}
	if !structuralPublicValue(record.Kind, maxRuntimeKindBytes) {
		return fmt.Errorf("kind is invalid")
	}
	switch record.State {
	case RuntimeOperationPrepared, RuntimeOperationApplying, RuntimeOperationTerminal:
	default:
		return fmt.Errorf("state is invalid")
	}
	if record.ExpectedGeneration > math.MaxInt64 {
		return fmt.Errorf("expected_generation exceeds storage range")
	}
	if !optionalStructuralPublicValue(record.ExpectedArchiveID, maxRuntimeArchiveIDBytes) ||
		!optionalStructuralPublicValue(record.TargetArchiveID, maxRuntimeArchiveIDBytes) ||
		!optionalStructuralPublicValue(record.TargetSnapshotID, maxRuntimeSnapshotIDBytes) ||
		!optionalStructuralPublicValue(record.TargetModel, maxEffectiveModelBytes) ||
		!optionalStructuralPublicValue(record.Outcome, maxRuntimeOutcomeBytes) ||
		!optionalStructuralPublicValue(record.ReasonCode, maxRuntimeOutcomeBytes) {
		return fmt.Errorf("runtime operation metadata is invalid")
	}
	if err := prepareOptionalRequestedModel(&record.TargetRequestedModel); err != nil {
		return err
	}
	switch RuntimeOperationKind(record.Kind) {
	case RuntimeOperationKindModel:
		// Nil is retained for model operations written before requested_model
		// existed. New writers always pin the target explicitly.
		if record.TargetRequestedModel != nil && *record.TargetRequestedModel != record.TargetModel {
			return fmt.Errorf("target_requested_model is invalid")
		}
	case RuntimeOperationKindLifetimePreference:
		if record.TargetRequestedModel != nil && *record.TargetRequestedModel != record.TargetModel {
			return fmt.Errorf("target_requested_model projection is invalid")
		}
	default:
		if record.TargetRequestedModel != nil {
			return fmt.Errorf("target_requested_model is not supported")
		}
	}
	if err := prepareTargetProviderBinding(&record.TargetProviderBindingJSON); err != nil {
		return err
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("runtime operation timestamps are required")
	}
	if (record.State == RuntimeOperationTerminal) != (record.TerminalAt != nil) {
		return fmt.Errorf("terminal_at does not match state")
	}
	return nil
}

func prepareTargetProviderBinding(raw *string) error {
	if *raw == "" {
		return nil
	}
	if len(*raw) > maxRuntimeProviderBindingJSONBytes {
		return fmt.Errorf("target_provider_binding_json is invalid")
	}
	canonical, err := providers.CanonicalBindingJSON(*raw)
	if err != nil || len(canonical) > maxRuntimeProviderBindingJSONBytes {
		return fmt.Errorf("target_provider_binding_json is invalid")
	}
	*raw = canonical
	return nil
}

func optionalStructuralPublicValue(value string, maxBytes int) bool {
	return value == "" || structuralPublicValue(value, maxBytes)
}

func structuralPublicValue(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
