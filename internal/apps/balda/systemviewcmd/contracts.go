// Package systemviewcmd defines the immutable, secret-free system projection.
package systemviewcmd

// Snapshot is the configured system view captured at Core startup.
type Snapshot struct {
	Capabilities []Capability
	Agent        Agent
}

// Capability describes one configured integration without claiming live health.
type Capability struct {
	ID         string
	Name       string
	Mode       string
	RouteCount int
	Settings   []Setting
	Endpoints  []Endpoint
}

// Setting is one explicit non-secret configured value.
type Setting struct{ Label, Value string }

// Endpoint is one canonical configured callback location.
type Endpoint struct{ Label, URL string }

// Agent is the allowlisted effective agent configuration shown read-only.
type Agent struct {
	ProviderID, ProviderType, Profile, Model, ReasoningEffort, GlobalInstruction string
}
