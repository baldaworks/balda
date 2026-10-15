package runtimecatalogcmd

import (
	"slices"
	"strings"
)

// ManagedSkillSelection is private host evidence for one complete desired managed
// selection. Empty Sources explicitly proves absence; Generation is independent
// of the catalog content hash. Never accept this evidence from browser input.
type ManagedSkillSelection struct {
	Generation uint64             `json:"generation"`
	Sources    []SourceDescriptor `json:"sources"`
}

// Normalized returns a deterministic, independent descriptor set.
func (s ManagedSkillSelection) Normalized() ManagedSkillSelection {
	s.Sources = slices.Clone(s.Sources)
	slices.SortFunc(s.Sources, func(a, b SourceDescriptor) int {
		if n := strings.Compare(string(a.ID.Kind), string(b.ID.Kind)); n != 0 {
			return n
		}
		if n := strings.Compare(a.ID.Name, b.ID.Name); n != 0 {
			return n
		}
		return strings.Compare(string(a.Revision), string(b.Revision))
	})
	return s
}

// Equal compares exact generation and the complete unordered descriptor set.
func (s ManagedSkillSelection) Equal(other ManagedSkillSelection) bool {
	return s.Generation == other.Generation && slices.Equal(s.Normalized().Sources, other.Normalized().Sources)
}
