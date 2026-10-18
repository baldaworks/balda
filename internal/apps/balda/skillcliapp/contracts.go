package skillcliapp

import (
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// Code is a stable, bounded CLI outcome code.
type Code string

const (
	CodeInvalid              Code = "invalid"
	CodeConflict             Code = "conflict"
	CodeNotOwned             Code = "not_owned"
	CodeNotFound             Code = "not_found"
	CodeAmbiguous            Code = "ambiguous"
	CodeUnavailable          Code = "unavailable"
	CodeRuntimeActive        Code = "runtime_active"
	CodeConfirmationRequired Code = "confirmation_required"
	CodeCancelled            Code = "cancelled"
	CodePublicationPending   Code = "publication_pending"
)

// Error contains only safe, actionable CLI information.
type Error struct {
	Code       Code     `json:"code"`
	Candidates []string `json:"candidates,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	switch e.Code {
	case CodeInvalid:
		return "invalid skill command input"
	case CodeConflict:
		return "skills changed; run the command again"
	case CodeNotOwned:
		return "skill is not managed by the standalone skill lifecycle"
	case CodeNotFound:
		return "skill not found"
	case CodeAmbiguous:
		return "skill name is ambiguous; use an exact ID"
	case CodeRuntimeActive:
		return "Balda is running for this state directory; stop it before changing skills"
	case CodeConfirmationRequired:
		return "confirmation is required"
	case CodeCancelled:
		return "operation cancelled"
	case CodePublicationPending:
		return "change was accepted but publication is pending; run balda skill recover"
	default:
		return "skill management is unavailable"
	}
}

func newError(code Code) *Error { return &Error{Code: code} }

// Entry is the stable, source-safe CLI skill projection.
type Entry struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Description     string             `json:"description,omitempty"`
	Ownership       skillcmd.Ownership `json:"ownership"`
	SourceLabel     string             `json:"source_label,omitempty"`
	PluginID        string             `json:"plugin_id,omitempty"`
	Enabled         bool               `json:"enabled"`
	Pending         bool               `json:"pending"`
	Revision        string             `json:"revision,omitempty"`
	CurrentRevision string             `json:"current_revision,omitempty"`
}

// Inventory is the public list result.
type Inventory struct {
	Entries     []Entry  `json:"entries"`
	Diagnostics []string `json:"diagnostics,omitempty"`
	Generation  uint64   `json:"generation"`
	Pending     bool     `json:"pending"`
}

// Review is the bounded confirmation payload.
type Review struct {
	Operation      string `json:"operation"`
	Entry          Entry  `json:"entry,omitempty"`
	SkillPath      string `json:"skill_path,omitempty"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
	Impact         string `json:"impact"`
}

// Result distinguishes durable acceptance from publication completion.
type Result struct {
	Operation       string `json:"operation"`
	Accepted        bool   `json:"accepted"`
	Published       bool   `json:"published"`
	Pending         bool   `json:"pending"`
	Generation      uint64 `json:"generation"`
	Entry           *Entry `json:"entry,omitempty"`
	RestartRequired bool   `json:"restart_required"`
	ResetRequired   bool   `json:"reset_required"`
}

func projectInventory(in skillcmd.Inventory) Inventory {
	out := Inventory{Diagnostics: append([]string(nil), in.Diagnostics...), Generation: in.Generation, Pending: in.Pending}
	for _, entry := range in.Entries {
		out.Entries = append(out.Entries, projectEntry(entry))
	}
	return out
}

func projectEntry(in skillcmd.Entry) Entry {
	sourceLabel := in.SourceLabel
	if in.Ownership == skillcmd.OwnershipManaged {
		sourceLabel = "Managed standalone skill"
	}
	return Entry{
		ID: publicID(in), Name: in.Name, Description: in.Description,
		Ownership: in.Ownership, SourceLabel: sourceLabel, PluginID: in.PluginID,
		Enabled: in.Enabled, Pending: in.Pending,
		Revision: string(in.Revision), CurrentRevision: string(in.CurrentRevision),
	}
}

func projectResult(operation skillcmd.Operation, in skillcmd.Outcome) Result {
	out := Result{
		Operation: string(operation), Accepted: in.Accepted,
		Published: in.Accepted && !in.Pending, Pending: in.Pending,
		Generation: in.Generation, RestartRequired: in.Accepted,
		ResetRequired: in.Accepted,
	}
	if in.Entry != nil {
		entry := projectEntry(*in.Entry)
		out.Entry = &entry
	}
	return out
}

// Envelope is the stable JSON command projection. Exactly one data field is
// populated for successful commands; failures populate Error.
type Envelope struct {
	OK        bool       `json:"ok"`
	Inventory *Inventory `json:"inventory,omitempty"`
	Entry     *Entry     `json:"entry,omitempty"`
	Review    *Review    `json:"review,omitempty"`
	Result    *Result    `json:"result,omitempty"`
	Error     *Error     `json:"error,omitempty"`
}

// AsError returns a public CLI error without exposing the wrapped cause.
func AsError(err error) (*Error, bool) {
	var public *Error
	if !errors.As(err, &public) {
		return nil, false
	}
	return public, true
}
