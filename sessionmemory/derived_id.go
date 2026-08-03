package sessionmemory

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

const derivedIDPrefix = "session-memory-derived:v1:"

// AtomItemID derives the stable logical identity of one normalized atom.
func AtomItemID(scope Scope, category AtomCategory, text string) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := category.Validate(); err != nil {
		return "", err
	}
	normalized, err := validateDerivedText("atom text", text)
	if err != nil {
		return "", err
	}
	return derivedStableID("atom", scope.Key, string(scope.Kind), string(category), normalized), nil
}

// ScenarioItemID derives the stable logical identity of one scope topic.
func ScenarioItemID(scope Scope, topicKey string) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	normalized, err := validateDerivedKey("scenario topic key", topicKey)
	if err != nil {
		return "", err
	}
	return derivedStableID("scenario", scope.Key, string(scope.Kind), normalized), nil
}

// ProfileItemID derives the single logical profile identity for one exact scope.
func ProfileItemID(scope Scope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	return derivedStableID("profile", scope.Key, string(scope.Kind)), nil
}

// DerivedRevisionID derives an immutable revision identity from content and provenance.
// Provenance order does not affect the result.
func DerivedRevisionID(
	scope Scope,
	itemID string,
	operationID string,
	contentParts []string,
	provenance Provenance,
	supersedes *RevisionRef,
) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if !isCanonicalID(itemID) {
		return "", invalidDerived("derived item id is required")
	}
	if !isCanonicalID(operationID) {
		return "", invalidDerived("derived operation id is required")
	}
	if len(contentParts) == 0 {
		return "", invalidDerived("derived revision content is required")
	}
	normalizedParts := make([]string, len(contentParts))
	for index, part := range contentParts {
		normalized, err := validateDerivedText("derived revision content", part)
		if err != nil {
			return "", err
		}
		normalizedParts[index] = normalized
	}
	if err := provenance.Validate(scope); err != nil {
		return "", err
	}
	if supersedes != nil {
		if err := supersedes.Validate(); err != nil {
			return "", err
		}
		if supersedes.ItemID != itemID {
			return "", invalidDerived("superseded revision must belong to the same item")
		}
	}

	parts := []string{scope.Key, string(scope.Kind), itemID, operationID}
	parts = append(parts, normalizedParts...)
	parts = append(parts, canonicalProvenanceParts(provenance)...)
	if supersedes != nil {
		parts = append(parts, "supersedes", supersedes.ItemID, supersedes.RevisionID)
	}
	return derivedStableID("revision", parts...), nil
}

func canonicalProvenanceParts(provenance Provenance) []string {
	raw := slices.Clone(provenance.RawSources)
	slices.SortFunc(raw, func(left, right SourceRef) int {
		leftParts := []string{left.Scope.Key, string(left.Scope.Kind), left.ExportID, left.SessionID, left.SourceTurnID}
		rightParts := []string{right.Scope.Key, string(right.Scope.Kind), right.ExportID, right.SessionID, right.SourceTurnID}
		for index := range leftParts {
			if order := cmp.Compare(leftParts[index], rightParts[index]); order != 0 {
				return order
			}
		}
		return 0
	})
	parents := slices.Clone(provenance.ParentRevisions)
	slices.SortFunc(parents, func(left, right RevisionRef) int {
		if order := cmp.Compare(left.ItemID, right.ItemID); order != 0 {
			return order
		}
		return cmp.Compare(left.RevisionID, right.RevisionID)
	})

	parts := make([]string, 0, len(raw)*6+len(parents)*3)
	for _, source := range raw {
		parts = append(parts,
			"raw",
			source.Scope.Key,
			string(source.Scope.Kind),
			source.ExportID,
			source.SessionID,
			source.SourceTurnID,
		)
	}
	for _, parent := range parents {
		parts = append(parts, "parent", parent.ItemID, parent.RevisionID)
	}
	return parts
}

func derivedStableID(kind string, parts ...string) string {
	hash := sha256.New()
	writeHashPart(hash, DerivedSchemaVersionV1)
	writeHashPart(hash, kind)
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return derivedIDPrefix + kind + ":" + hex.EncodeToString(hash.Sum(nil))
}
