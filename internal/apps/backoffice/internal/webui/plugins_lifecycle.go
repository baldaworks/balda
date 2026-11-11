package webui

import (
	"net/url"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// PluginDetailView keeps selection, activation and runtime evidence separate.
type PluginDetailView struct {
	plugincmd.PluginDetail
	DetailPath                    string
	CurrentCatalog, RuntimeHealth string
	MutationBlocked               bool
	CanEnable                     bool
	ContributionTables            []PluginContributionTable
	Diagnostics                   []string
}

// PluginReview presents an exact accepted candidate before a mutation.
type PluginReview struct {
	Upgrade              bool
	Marketplaces         []plugincmd.MarketplaceView
	Marketplace          string
	Candidates           []plugincmd.CandidateView
	Candidate            *plugincmd.CandidateView
	Current              *PluginDetailView
	ActionPath           string
	ExistingPath         string
	CandidateDiagnostics []string
}

// ProjectPluginDetail uses bounded health labels without inferring readiness from Enabled.
func ProjectPluginDetail(item plugincmd.PluginDetail) *PluginDetailView {
	view := &PluginDetailView{PluginDetail: item, DetailPath: PluginDetailPath(item.Name), CurrentCatalog: "Selected revision is not current", RuntimeHealth: "Unknown"}
	if item.Runtime.RevisionCurrent {
		view.CurrentCatalog = "Selected revision is current"
	}
	if item.Runtime.SnapshotID != "" && item.Runtime.RevisionCurrent {
		switch {
		case item.Runtime.DegradedMCPServers > 0:
			view.RuntimeHealth = "Degraded"
		case item.Runtime.ReadyMCPServers < item.Runtime.DesiredMCPServers:
			view.RuntimeHealth = "MCP attachments pending"
		case item.Runtime.DesiredMCPServers > 0:
			view.RuntimeHealth = "MCP attachments ready"
		default:
			view.RuntimeHealth = "No MCP attachments"
		}
	}
	for _, revision := range item.Revisions {
		if revision.Current && revision.Available {
			view.CanEnable = true
		}
	}
	view.ContributionTables = []PluginContributionTable{{Label: "Commands", Items: item.Commands}, {Label: "Skills", Items: item.Skills}, {Label: "MCP servers", Items: item.MCPServers}}
	view.Diagnostics = ProjectPluginDiagnostics(item.DiagnosticCodes)
	view.Diagnostics = append(view.Diagnostics, ProjectPluginDiagnostics(item.Runtime.DiagnosticCodes)...)
	return view
}

// PluginDetailPath separates logical plugin names from section and action routes.
func PluginDetailPath(name string) string { return "/plugins/installed/" + url.PathEscape(name) }

// PluginContributionTable contains only safe contribution metadata.
type PluginContributionTable struct {
	Label string
	Items []plugincmd.Contribution
}

func pluginDiagnosticLabel(code string) string {
	switch code {
	case "revision_unavailable":
		return "Selected revision is unavailable."
	case "origin_unavailable", "origin_unverified":
		return "The locked origin cannot be verified."
	case "mcp_config_invalid":
		return "An MCP server definition is invalid."
	case "plugin_extension_invalid":
		return "The plugin extension is invalid."
	case "skill_invalid", "skill_component_invalid":
		return "A skill could not be loaded."
	case "manifest_field_ignored":
		return "An unsupported manifest field was ignored."
	default:
		return "Additional package or runtime diagnostics are present."
	}
}

// ProjectPluginDiagnostics maps owner codes to bounded public explanations.
func ProjectPluginDiagnostics(codes []string) []string {
	var labels []string
	for _, code := range codes {
		labels = append(labels, pluginDiagnosticLabel(code))
	}
	return labels
}
