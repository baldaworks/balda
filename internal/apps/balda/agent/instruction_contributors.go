package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const (
	// DefaultMaxSessionInstructionContributorBytes bounds one trusted extension.
	DefaultMaxSessionInstructionContributorBytes = 256 << 10
	// DefaultMaxSessionInstructionContributionsBytes bounds all extension content.
	DefaultMaxSessionInstructionContributionsBytes = 1 << 20
)

// SessionInstructionContext is the provider-neutral, immutable context exposed
// to trusted host instruction contributors while a session runtime is built.
type SessionInstructionContext struct {
	SnapshotID   runtimecatalogcmd.SnapshotID
	SessionID    string
	ChannelType  string
	WorkspaceDir string
	ProviderID   string
	Skills       SkillMetadataProjection
}

// SessionInstructionContributor adds one bounded section to the instruction
// assembled before a provider adapter is selected. Implementations are trusted
// host extensions; portable plugin data must not implement this interface.
type SessionInstructionContributor interface {
	ID() string
	ContributeSessionInstruction(ctx context.Context, input SessionInstructionContext) (string, error)
}

func assembleSessionInstruction(
	ctx context.Context,
	base string,
	input SessionInstructionContext,
	contributors []SessionInstructionContributor,
	maxTotalBytes int,
) (string, error) {
	base = strings.TrimSpace(base)
	if maxTotalBytes < 0 {
		return "", errors.New("session instruction contribution total size limit must not be negative")
	}
	if maxTotalBytes == 0 {
		maxTotalBytes = DefaultMaxSessionInstructionContributionsBytes
	}

	ordered := append([]SessionInstructionContributor(nil), contributors...)
	for _, contributor := range ordered {
		if contributor == nil {
			return "", errors.New("session instruction contributor is nil")
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.TrimSpace(ordered[i].ID()) < strings.TrimSpace(ordered[j].ID())
	})

	seen := make(map[string]struct{}, len(ordered))
	var result strings.Builder
	contributedBytes := 0
	result.WriteString(base)
	for _, contributor := range ordered {
		id := strings.TrimSpace(contributor.ID())
		if !validInstructionContributorID(id) {
			return "", fmt.Errorf("invalid session instruction contributor id %q", id)
		}
		if _, exists := seen[id]; exists {
			return "", fmt.Errorf("duplicate session instruction contributor id %q", id)
		}
		seen[id] = struct{}{}

		content, err := contributor.ContributeSessionInstruction(ctx, cloneSessionInstructionContext(input))
		if err != nil {
			return "", fmt.Errorf("contribute session instruction %q: %w", id, err)
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if len(content) > DefaultMaxSessionInstructionContributorBytes {
			return "", fmt.Errorf("session instruction contributor %q exceeds size limit", id)
		}
		if contributedBytes+len(content) > maxTotalBytes {
			return "", fmt.Errorf("session instruction contributions exceed total size limit after contributor %q", id)
		}
		section := "\n\nExtension instruction [" + id + "]:\n" + content
		result.WriteString(section)
		contributedBytes += len(content)
	}
	return result.String(), nil
}

func cloneSessionInstructionContext(input SessionInstructionContext) SessionInstructionContext {
	input.Skills.Skills = append([]SkillPromptMetadata(nil), input.Skills.Skills...)
	return input
}

func validInstructionContributorID(id string) bool {
	if id == "" {
		return false
	}
	for index, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (index > 0 && (r == '.' || r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}
