package runtimecatalog

import (
	"encoding/json"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// MetadataBudget limits one skill metadata projection. Non-positive values are unlimited.
type MetadataBudget struct {
	MaxItems int
	MaxBytes int
}

// SkillMetadataSelection contains whole descriptors that fit a deterministic budget.
type SkillMetadataSelection struct {
	Skills      []runtimecatalogcmd.SkillMetadata
	Diagnostics []runtimecatalogcmd.Diagnostic
	Omitted     int
}

// SelectSkillMetadata applies a deterministic whole-descriptor budget.
func SelectSkillMetadata(snapshot runtimecatalogcmd.Snapshot, budget MetadataBudget) SkillMetadataSelection {
	ordered := make([]runtimecatalogcmd.SkillMetadata, 0, len(snapshot.Skills))
	for _, skill := range snapshot.Skills {
		ordered = append(ordered, skill)
	}
	sort.Slice(ordered, func(i, j int) bool { return contributionKey(ordered[i].ID) < contributionKey(ordered[j].ID) })

	var result SkillMetadataSelection
	usedBytes := 0
	for _, skill := range ordered {
		data, _ := json.Marshal(skill)
		itemLimited := budget.MaxItems > 0 && len(result.Skills) >= budget.MaxItems
		bytesLimited := budget.MaxBytes > 0 && usedBytes+len(data) > budget.MaxBytes
		if itemLimited || bytesLimited {
			id := skill.ID
			result.Omitted++
			result.Diagnostics = append(result.Diagnostics, runtimecatalogcmd.Diagnostic{
				Severity:     runtimecatalogcmd.DiagnosticSeverityInfo,
				Code:         runtimecatalogcmd.DiagnosticSkillMetadataOmitted,
				Source:       skill.ID.Source,
				Contribution: &id,
			})
			continue
		}
		result.Skills = append(result.Skills, skill)
		usedBytes += len(data)
	}
	return result
}
