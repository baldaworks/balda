package webui

import (
	"encoding/base64"
	"encoding/json"
	"net/url"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// SkillsView contains metadata-only inventory and ephemeral reviewed projections.
type SkillsView struct {
	Rows            []SkillRow
	ExpectedState   string
	Pending         bool
	Unavailable     bool
	Diagnostics     []string
	Detail          *SkillRow
	Install         bool
	Review          *skillcmd.Candidate
	Update          bool
	MutationBlocked bool
}

// SkillRow keeps structured identity separate from display names and URLs.
type SkillRow struct {
	skillcmd.Entry
	DetailPath string
	PluginPath string
}

// SkillDetailPath encodes structured contribution identity without parsing labels.
func SkillDetailPath(entry skillcmd.Entry) string {
	if entry.Ownership == skillcmd.OwnershipManaged {
		return "/skills/managed/" + url.PathEscape(entry.ID.Source.Name)
	}
	data, _ := json.Marshal(entry.ID)
	return "/skills/contributions/" + base64.RawURLEncoding.EncodeToString(data)
}

// SkillContributionToken resolves a bounded opaque structured detail token.
func SkillContributionToken(token string) (runtimecatalogcmd.ContributionID, error) {
	var id runtimecatalogcmd.ContributionID
	if len(token) > 4096 {
		return id, skillcmd.ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return id, skillcmd.ErrInvalid
	}
	if err := json.Unmarshal(data, &id); err != nil {
		return id, skillcmd.ErrInvalid
	}
	if id.Kind != runtimecatalogcmd.ContributionKindSkill || id.Source.Name == "" || id.Name == "" {
		return id, skillcmd.ErrInvalid
	}
	return id, nil
}

// ProjectSkills projects escaped metadata and safe navigation only.
func ProjectSkills(inventory skillcmd.Inventory) *SkillsView {
	view := &SkillsView{ExpectedState: inventory.ExpectedState, Pending: inventory.Pending}
	for _, entry := range inventory.Entries {
		view.Rows = append(view.Rows, ProjectSkillRow(entry))
	}
	// Diagnostics are selected messages, never arbitrary backend text.
	for _, code := range inventory.Diagnostics {
		switch code {
		case "refresh-unavailable":
			view.Diagnostics = append(view.Diagnostics, "The last refresh failed. The accepted catalog remains available.")
		case "skill_invalid", "skill_component_invalid":
			view.Diagnostics = append(view.Diagnostics, "Some skill sources could not be loaded. Check the source and refresh again.")
		default:
			view.Diagnostics = append(view.Diagnostics, "The catalog reported a contribution diagnostic. Qualified skill references remain available.")
		}
	}
	counts := make(map[string]int)
	for _, entry := range inventory.Entries {
		counts[entry.Name]++
	}
	for _, count := range counts {
		if count > 1 {
			view.Diagnostics = append(view.Diagnostics, "Duplicate skill names exist. Select a qualified source when invoking a skill.")
			break
		}
	}
	return view
}

// ProjectSkillRow projects the owning package without copying plugin resources.
func ProjectSkillRow(entry skillcmd.Entry) SkillRow {
	row := SkillRow{Entry: entry, DetailPath: SkillDetailPath(entry)}
	if entry.Ownership == skillcmd.OwnershipPlugin {
		row.PluginPath = PluginDetailPath(entry.PluginID)
	}
	return row
}
