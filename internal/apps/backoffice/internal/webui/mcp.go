package webui

import (
	"net/url"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// MCPView contains only the safe inventory and editor presentation.
type MCPView struct {
	Rows         []MCPRow
	Editor       *MCPEditor
	Device       *MCPDevice
	ProbeMessage string
}

// MCPRow keeps stored authorization separate from observed runtime readiness.
type MCPRow struct {
	ID, PublicID, Source, Transport, Endpoint, Targets, Status, Authorization, Recovery string
	DetailPath                                                                          string
	RevisionID                                                                          string
	Version                                                                             uint64
	ToolCount                                                                           int
	Ready, Enabled, ReadOnly, Deleted                                                   bool
}

// ProjectMCPRow constructs a bounded, secret-free inventory projection.
func ProjectMCPRow(item mcpcmd.Item) MCPRow {
	row := MCPRow{ID: item.Connection.ID, PublicID: item.Connection.PublicID, Version: item.Connection.Version,
		DetailPath: "/mcp/connections/" + url.PathEscape(item.Connection.ID), RevisionID: item.Connection.CurrentRevisionID, Enabled: item.Connection.Enabled, Deleted: item.Connection.Deleted,
		ReadOnly: item.Connection.Source != mcpcmd.SourceManaged, ToolCount: item.ToolCount, Ready: item.Status == mcpcmd.StatusReady}
	row.Source = "Backoffice"
	if row.ReadOnly {
		row.Source = "Configuration"
	}
	row.Transport = string(item.Definition.Transport)
	row.Endpoint = item.Definition.URL
	if item.Definition.Transport == mcpcmd.TransportStdio {
		row.Endpoint = item.Definition.Command
	}
	row.Targets = strings.Join(item.Definition.Targets.Providers, ", ")
	if item.Definition.Targets.All {
		row.Targets = "All providers"
	}
	switch item.Status {
	case mcpcmd.StatusReady:
		row.Status = "Ready"
	case mcpcmd.StatusPending:
		row.Status = "Pending"
	case mcpcmd.StatusDisabled:
		row.Status = "Disabled"
	case mcpcmd.StatusDeleted:
		row.Status = "Deleted"
	case mcpcmd.StatusConflict:
		row.Status = "Conflict"
	case mcpcmd.StatusAuthRequired:
		row.Status = "Authorization required"
	case mcpcmd.StatusDisconnected:
		row.Status = "Disconnected"
	default:
		row.Status = "Unavailable"
	}
	switch item.Authorization {
	case mcpcmd.GrantAuthorized:
		row.Authorization = "Authorized"
	case mcpcmd.GrantAuthRequired:
		row.Authorization = "Authorization required"
	case mcpcmd.GrantDisconnected:
		row.Authorization = "Disconnected"
	}
	switch item.Recovery {
	case mcpcmd.RecoveryAuthorizationRequired:
		row.Recovery = "Worker authorization required"
	case mcpcmd.RecoveryFirstAuthorization:
		row.Recovery = "First worker authorization required"
	case mcpcmd.RecoveryCaptureRequired:
		row.Recovery = "Current configuration capture required"
	}
	return row
}

// MCPEditor contains definition metadata only. All binding inputs are blank.
type MCPEditor struct {
	Row                                                        MCPRow
	New                                                        bool
	AuthorizationAvailable                                     bool
	Attempt                                                    *MCPAttempt
	PublicID, Transport, Command, Args, Directory, URL, Scopes string
	Enabled, OAuth, All                                        bool
	Providers                                                  []MCPProvider
	Env, Headers                                               []MCPValueRow
	Action                                                     string
}
type MCPProvider struct {
	ID       string
	Selected bool
}
type MCPValueRow struct{ Key, Kind, Operation string }

func ProjectMCPEditor(item mcpcmd.Item, providers []string, create bool) *MCPEditor {
	d := item.Definition
	e := &MCPEditor{Row: ProjectMCPRow(item), New: create, PublicID: item.Connection.PublicID, Transport: string(d.Transport), Command: d.Command, Args: strings.Join(d.Args, "\n"), Directory: d.Directory, URL: d.URL, Scopes: strings.Join(d.Scopes, "\n"), Enabled: item.Connection.Enabled, OAuth: d.OAuth, All: d.Targets.All, Action: "/mcp/connections/" + url.PathEscape(item.Connection.ID)}
	if create {
		e.Transport = "stdio"
		e.Enabled = true
		e.All = true
		e.Row.ReadOnly = false
		e.Action = "/mcp/connections"
	}
	e.AuthorizationAvailable = !create && !item.Connection.Deleted && (d.Transport == mcpcmd.TransportHTTP || d.Transport == mcpcmd.TransportSSE) && (d.OAuth || item.Recovery != "")
	for _, id := range providers {
		selected := false
		for _, target := range d.Targets.Providers {
			if id == target {
				selected = true
			}
		}
		e.Providers = append(e.Providers, MCPProvider{ID: id, Selected: selected})
	}
	e.Env = mcpValueRows(d.Env, !e.Row.ReadOnly)
	e.Headers = mcpValueRows(d.Headers, !e.Row.ReadOnly)
	return e
}
func mcpValueRows(bindings map[string]mcpcmd.ValueBinding, edit bool) []MCPValueRow {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]MCPValueRow, 0, len(keys)+2)
	for _, key := range keys {
		rows = append(rows, MCPValueRow{Key: key, Kind: string(bindings[key].Kind), Operation: "keep"})
	}
	if edit {
		rows = append(rows, MCPValueRow{Kind: "literal", Operation: "set"}, MCPValueRow{Kind: "protected", Operation: "set"})
	}
	return rows
}

// MCPValueForm supplies stable labels for one write-only binding row.
type MCPValueForm struct {
	Row    MCPValueRow
	Prefix string
	Index  int
}

func mcpValueForm(row MCPValueRow, prefix string, index int) MCPValueForm {
	return MCPValueForm{Row: row, Prefix: prefix, Index: index}
}

// MCPAttempt projects a safe cancellation/status control for an active attempt.
type MCPAttempt struct {
	ID, CancelPath, StatusPath, Expires string
	Device                              bool
}

// MCPDevice contains native one-time instructions or safe terminal status.
type MCPDevice struct {
	Status, Message, UserCode, VerificationURI, VerificationURIComplete string
	DetailPath, StatusPath, CancelPath, Expires                         string
	Pending, Instructions                                               bool
}

// ProjectMCPDevice reveals verification instructions only in the begin response.
// Status/history GETs expose progress and metadata, never another issuance.
func ProjectMCPDevice(device mcpcmd.DeviceAuthorization, instructions bool) *MCPDevice {
	view := &MCPDevice{Status: string(device.Status), Pending: device.Status == mcpcmd.DevicePending, DetailPath: "/mcp/connections/" + url.PathEscape(device.ConnectionID), StatusPath: "/mcp/oauth/device/" + url.PathEscape(device.ID), CancelPath: "/mcp/oauth/attempts/" + url.PathEscape(device.ID) + "/cancel", Expires: device.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")}
	switch device.Status {
	case mcpcmd.DeviceAuthorized:
		view.Message = "Worker authorization was saved. Open the connection to inspect tool readiness or retry attachment; authorization does not mean Ready."
	case mcpcmd.DeviceDenied:
		view.Message = "Worker authorization was denied. Reopen the connection and start again."
	case mcpcmd.DeviceExpired:
		view.Message = "Worker authorization expired. Reopen the connection and start again."
	case mcpcmd.DeviceFailed:
		view.Message = "Worker authorization could not complete. Reopen the connection and start again."
	default:
		view.Message = "Worker authorization is pending. Instructions are shown once when the attempt starts. If they are lost, cancel and start again."
	}
	if instructions && view.Pending {
		view.Instructions = true
		view.UserCode = device.UserCode
		view.VerificationURI = device.VerificationURI
		view.VerificationURIComplete = device.VerificationURIComplete
	}
	return view
}
