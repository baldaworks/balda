package webui

// ProvidersView is the provider inventory or one bounded editor.
type ProvidersView struct {
	Version      uint64
	Rows         []ProviderRow
	Editor       *ProviderEditor
	Lifecycle    LifecycleView
	EmptyMessage string
}

// ProviderRow is a secret-free provider summary.
type ProviderRow struct {
	ID           string
	Source       string
	Type         string
	ModelOrMode  string
	Members      string
	State        string
	Revision     string
	Reproducible bool
	ReadOnly     bool
	Deleted      bool
	Enabled      bool
	DetailPath   string
	Version      uint64
}

type ProviderTypeOption struct {
	Value, Label string
	Selected     bool
}

// ProviderEditor contains public values and protected-value presence only.
type ProviderEditor struct {
	Row                     ProviderRow
	New                     bool
	SelectType              bool
	Action                  string
	Types                   []ProviderTypeOption
	Type                    string
	DriverClass             string
	ShowAPIKey              bool
	RequireAPIKey           bool
	ShowEnvironment         bool
	ShowReasoning           bool
	ShowTimeout             bool
	ShowBridgeVersion       bool
	ShowRegistryID          bool
	ID                      string
	Model                   string
	ReasoningEffort         string
	Mode                    string
	BridgeVersion           string
	RegistryID              string
	ModelConfigID           string
	ReasoningEffortConfigID string
	ExtraArgs               string
	InheritEnv              string
	MCPServers              string
	SystemInstructions      string
	Timeout                 string
	PoolMembers             string
	HasAPIKey               bool
	EnvironmentNames        []string
}
