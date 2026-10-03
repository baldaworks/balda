package mcpcmd

// Status distinguishes durable selection from observed runtime readiness.
type Status string

const (
	StatusPending      Status = "pending"
	StatusReady        Status = "ready"
	StatusUnavailable  Status = "unavailable"
	StatusDisabled     Status = "disabled"
	StatusDeleted      Status = "deleted"
	StatusConflict     Status = "conflict"
	StatusAuthRequired Status = "auth_required"
	StatusDisconnected Status = "disconnected"
)

// Item is a secret-free inventory row; configured values are redacted.
type Item struct {
	Connection Connection `json:"connection"`
	Definition Definition `json:"definition"`
	Status     Status     `json:"status"`
	ToolCount  int        `json:"tool_count"`
}

// CreateDefinition creates a managed identity. Values are write-only edits.
type CreateDefinition struct {
	PublicID   string
	Definition Definition
	Values     ValueEdits
	Enabled    bool
	Authority  Authority
}

// UpdateDefinition preserves the immutable public identity.
type UpdateDefinition struct {
	ConnectionID    string
	ExpectedVersion uint64
	Definition      Definition
	Values          ValueEdits
	Enabled         bool
	Authority       Authority
}

// ChangeSelection disables, enables or tombstones a managed connection.
type ChangeSelection struct {
	ConnectionID    string
	ExpectedVersion uint64
	Enabled         bool
	Authority       Authority
}
