package sessionmcp

type noInput struct{}

type sessionLocatorInput struct {
	SessionID   string `json:"session_id" jsonschema:"target session id, e.g. tg-123-0"`
	ChannelType string `json:"channel_type" jsonschema:"channel type, e.g. telegram"`
	AddressKey  string `json:"address_key" jsonschema:"canonical address key, e.g. 12345:0"`
	AddressJSON string `json:"address_json" jsonschema:"canonical address JSON for the target session locator"`
}

// ToolError represents an error from a tool operation.
type ToolError struct {
	Operation string `json:"operation" jsonschema:"tool name that produced the error"`
	Code      string `json:"code" jsonschema:"stable machine-readable error code"`
	Message   string `json:"message" jsonschema:"human-readable error message"`
}

// ToolOutcome represents the result of a tool operation.
type ToolOutcome struct {
	OK    bool       `json:"ok" jsonschema:"true when the tool completed successfully"`
	Error *ToolError `json:"error,omitempty" jsonschema:"error details when ok is false"`
}

type basicOutput struct {
	ToolOutcome
}

// Get input/output

type getKeyInput struct {
	Key string `json:"key" jsonschema:"state key to retrieve"`
}

type getKeyOutput struct {
	ToolOutcome
	Value string `json:"value,omitempty" jsonschema:"stored value for the key"`
	Found bool   `json:"found" jsonschema:"whether the key exists"`
}

// Set input/output

type setKeyInput struct {
	Key   string `json:"key" jsonschema:"state key"`
	Value string `json:"value" jsonschema:"state value (JSON string)"`
}

// Delete input/output

type deleteKeyInput struct {
	Key string `json:"key" jsonschema:"state key to delete"`
}

// List input/output

type listKeysInput struct {
	Prefix string `json:"prefix,omitempty" jsonschema:"optional key prefix filter"`
}

type listKeysOutput struct {
	ToolOutcome
	Keys []string `json:"keys,omitempty" jsonschema:"matching keys"`
}

// GetJSON input/output - returns parsed JSON value

type getJSONInput struct {
	Key string `json:"key" jsonschema:"state key to retrieve as JSON"`
}

type getJSONOutput struct {
	ToolOutcome
	Value interface{} `json:"value,omitempty" jsonschema:"parsed JSON value stored at the key"`
	Found bool        `json:"found" jsonschema:"whether the key exists"`
}

// SetJSON input/output - stores a value as JSON

type setJSONInput struct {
	Key   string      `json:"key" jsonschema:"state key"`
	Value interface{} `json:"value" jsonschema:"value to store as JSON"`
}

// MergeJSON input/output - merges JSON object into existing value

type mergeJSONInput struct {
	Key   string                 `json:"key" jsonschema:"state key (must contain JSON object)"`
	Value map[string]interface{} `json:"value" jsonschema:"fields to merge into existing object"`
}

type mergeJSONOutput struct {
	ToolOutcome
	Merged map[string]interface{} `json:"merged,omitempty" jsonschema:"merged JSON object after applying the update"`
}

// Keyspace input/output - for scoping keys to a session/agent

type keyspaceInput struct {
	Namespace string `json:"namespace" jsonschema:"namespace for key isolation (e.g., session-id or agent-name)"`
	Key       string `json:"key" jsonschema:"key within namespace"`
}

type keyspaceValueInput struct {
	Namespace string `json:"namespace" jsonschema:"namespace for key isolation"`
	Key       string `json:"key" jsonschema:"key within namespace"`
	Value     string `json:"value" jsonschema:"value to store"`
}

type keyspaceJSONInput struct {
	Namespace string      `json:"namespace" jsonschema:"namespace for key isolation"`
	Key       string      `json:"key" jsonschema:"key within namespace"`
	Value     interface{} `json:"value" jsonschema:"value to store as JSON"`
}

type namespaceOnlyInput struct {
	Namespace string `json:"namespace" jsonschema:"namespace to list keys from"`
}

type sessionWaitInput struct {
	Locator      sessionLocatorInput `json:"locator" jsonschema:"target session locator for the wake-up"`
	Content      string              `json:"content" jsonschema:"message or command text to run when the wait wakes"`
	DelaySeconds int                 `json:"delay_seconds" jsonschema:"positive delay in seconds before wake-up"`
	JobID        string              `json:"job_id,omitempty" jsonschema:"optional stable wait id"`
	RequestedBy  string              `json:"requested_by,omitempty" jsonschema:"optional requester id for audit metadata"`
	Notify       bool                `json:"notify,omitempty" jsonschema:"whether to send an acknowledgement message to the session when the wait is scheduled"`
}

type sessionWaitOutput struct {
	ToolOutcome
	Accepted bool   `json:"accepted" jsonschema:"true when the wait command was published successfully"`
	Message  string `json:"message,omitempty" jsonschema:"human-readable status message"`
}
