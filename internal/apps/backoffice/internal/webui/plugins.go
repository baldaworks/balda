package webui

import (
	"net/url"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// PluginsView contains one local tab of global plugin management.
type PluginsView struct {
	PluginDetail      *PluginDetailView
	Review            *PluginReview
	Unavailable       bool
	Marketplaces      bool
	InstalledRows     []plugincmd.InstalledView
	MarketplaceRows   []PluginMarketplaceRow
	MarketplaceAdd    *PluginMarketplaceAdd
	MarketplaceDetail *PluginMarketplaceDetail
}

// PluginMarketplaceAdd intentionally has no Source value to refill.
type PluginMarketplaceAdd struct{}

// PluginMarketplaceRow uses the same field labels as the definition form.
type PluginMarketplaceRow struct {
	Name, Source, Ref, Sparse, DetailPath, ExpectedState string
	Failure, LastAttempt, LastSuccessful                 string
	Failed                                               bool
	AvailablePlugins                                     int
	ResolvedRevision                                     string
}

// PluginMarketplaceDetail keeps accepted discovery distinct from current attempts.
type PluginMarketplaceDetail struct {
	Row              PluginMarketplaceRow
	Candidates       []PluginMarketplaceCandidate
	FailureMessage   string
	DiscoveryMessage string
}

// ProjectPluginMarketplaceRow retains only safe, already-projected definition data.
func ProjectPluginMarketplaceRow(item plugincmd.MarketplaceView) PluginMarketplaceRow {
	row := PluginMarketplaceRow{AvailablePlugins: item.AvailablePlugins, ResolvedRevision: item.ResolvedRevision, Name: item.Name, Source: item.Source, Ref: item.Ref, Sparse: strings.Join(item.Sparse, "\n"), ExpectedState: item.ExpectedState, DetailPath: "/plugins/marketplaces/definitions/" + url.PathEscape(item.Name), Failed: item.LastAttemptStatus == "failed"}
	if row.Failed {
		row.Failure = pluginDiscoveryFailure(item.FailureCode)
	}
	if !item.LastAttemptAt.IsZero() {
		row.LastAttempt = item.LastAttemptAt.UTC().Format("2006-01-02 15:04:05 UTC") + " · " + item.LastAttemptStatus
		if row.Failure != "" {
			row.LastAttempt += " · " + row.Failure
		}
	}
	if !item.LastSuccessfulAt.IsZero() {
		row.LastSuccessful = item.LastSuccessfulAt.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return row
}

// ProjectPluginMarketplaceDetail preserves stale evidence without actionable tokens.
func ProjectPluginMarketplaceDetail(item plugincmd.MarketplaceDetail, installed []plugincmd.InstalledView) *PluginMarketplaceDetail {
	detail := &PluginMarketplaceDetail{Row: ProjectPluginMarketplaceRow(item.MarketplaceView)}
	for _, candidate := range item.Candidates {
		row := PluginMarketplaceCandidate{CandidateView: candidate, ReviewLabel: "Available"}
		if detail.Row.Failed {
			row.CandidateToken = ""
			row.ReviewLabel = "Refresh required"
		} else if candidate.CandidateToken == "" {
			row.ReviewLabel = "Unavailable"
		}
		if !detail.Row.Failed && candidate.CandidateToken == "" && len(candidate.DiagnosticCodes) > 0 {
			row.Diagnostic = "This package could not be validated."
		}
		for _, plugin := range installed {
			if plugin.Name == candidate.Name {
				row.Installed = true
				row.InstalledPath = PluginDetailPath(plugin.Name)
				break
			}
		}
		detail.Candidates = append(detail.Candidates, row)
	}
	for _, code := range item.DiagnosticCodes {
		if code == "invalid_plugin_entry" {
			detail.DiscoveryMessage = "Some marketplace entries could not be validated and were excluded."
			break
		}
	}
	if detail.Row.Failed {
		detail.FailureMessage = detail.Row.Failure + ". The last refresh failed. Earlier discovery is shown for reference. Refresh successfully before reviewing a plugin."
	}
	return detail
}

// PluginMarketplaceCandidate separates stale discovery from an invalid package.
type PluginMarketplaceCandidate struct {
	plugincmd.CandidateView
	ReviewLabel, Diagnostic string
	Installed               bool
	InstalledPath           string
}

func pluginDiscoveryFailure(code string) string {
	switch code {
	case "invalid_index":
		return "Invalid marketplace index"
	case "duplicate_plugin_name":
		return "Duplicate plugin name"
	case "source_unavailable":
		return "Source unavailable"
	default:
		return "Discovery unavailable"
	}
}
