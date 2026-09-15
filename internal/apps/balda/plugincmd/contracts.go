package plugincmd

// PluginSummary is the transport-neutral installed-plugin view.
type PluginSummary struct {
	Name        string
	Version     string
	Description string
}

// AvailablePlugin is the transport-neutral marketplace-plugin view.
type AvailablePlugin struct {
	Name             string
	DisplayName      string
	Description      string
	Version          string
	Marketplace      string
	MarketplaceLabel string
	Category         string
	Installed        bool
	DiagnosticCodes  []string
}

// MarketplaceSource describes a configured marketplace without host paths.
type MarketplaceSource struct {
	Name   string
	Source string
	Ref    string
	Sparse []string
}

// MarketplaceStatus is the existing marketplace status presentation contract.
type MarketplaceStatus struct {
	Name             string
	Source           string
	Kind             string
	Ref              string
	Sparse           []string
	Cached           bool
	LastRefreshedAt  string
	ResolvedRef      string
	ManifestPresent  bool
	AvailablePlugins int
}

// MarketplaceUpgradeResult describes one marketplace refresh.
type MarketplaceUpgradeResult struct {
	Name        string
	Source      string
	PluginCount int
	Refreshed   bool
	Status      MarketplaceStatus
}

// CapabilitySummary is a stable count-only capability view.
type CapabilitySummary struct {
	Commands    int `json:"commands"`
	Skills      int `json:"skills"`
	MCPServers  int `json:"mcp_servers"`
	Diagnostics int `json:"diagnostics"`
}

// CapabilityDiff compares the active and selected marketplace revisions.
type CapabilityDiff struct {
	Active    CapabilitySummary
	Candidate CapabilitySummary
}

// RuntimeStatus is bounded runtime-catalog and projection health.
type RuntimeStatus struct {
	SnapshotID          string
	SnapshotSequence    uint64
	ProjectionLag       uint64
	Advertisements      int
	Skills              int
	SkillAmbiguities    int
	DesiredMCPServers   int
	ReadyMCPServers     int
	DegradedMCPServers  int
	DiagnosticCodes     []string
	ProjectionOmissions []string
}

// PluginStatus is the non-secret owner inspection view for an installed plugin.
type PluginStatus struct {
	Name         string
	Version      string
	Marketplace  string
	Origin       string
	Revision     string
	Enabled      bool
	Drifted      bool
	Capabilities CapabilitySummary
	Runtime      RuntimeStatus
}
