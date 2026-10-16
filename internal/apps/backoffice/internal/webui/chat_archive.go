package webui

import (
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

const archiveUnknown = "Unknown"
const archiveUserRole = "user"

// ChatArchiveView allowlists browser-visible historical metadata and chat text.
type ChatArchiveView struct {
	Query, Channel, Status, BackPath, OlderPath string
	Rows                                        []ChatSessionRow
	Detail                                      *ChatSessionRow
	Messages                                    []ChatMessageRow
	PreviousPath, NextPath                      string
}

// ChatSessionRow excludes runtime identities, boot identifiers and execution data.
type ChatSessionRow struct {
	Label, Channel, Locator, Title, Audience, Subdivision, SubdivisionLabel, Person string
	Status, EndReason, Started, Activity, Ended, CoverageStart, DetailPath          string
	MessageCount                                                                    int64
	Partial                                                                         bool
}

// ChatMessageRow contains only visible role, historical author, time and text labels.
type ChatMessageRow struct {
	Role, Speaker, Principal, Username, At, Text string
	Attachments                                  []ChatAttachment
}

// ChatAttachment is a non-interactive safe attachment label.
type ChatAttachment struct{ Name, Kind string }

func archiveTimeLabel(t time.Time) string {
	if t.IsZero() {
		return archiveUnknown
	}
	return t.UTC().Format("02 Jan 2006 · 15:04:05 UTC")
}
func knownArchiveLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return archiveUnknown
	}
	return value
}
func archiveSpeaker(s chatarchivecmd.Speaker) string {
	if s.Name != "" {
		return s.Name
	}
	if s.Username != "" {
		return "@" + s.Username
	}
	if s.Principal != "" {
		return s.Principal
	}
	return "Unknown speaker"
}
func archiveRow(summary chatarchivecmd.Summary, query string) ChatSessionRow {
	s := summary.Session
	activity := s.ObservedAt
	if activity.IsZero() {
		activity = s.StartedAt
	}
	status := archiveUnknown
	switch s.Status {
	case chatarchivecmd.StatusActive:
		status = "Active lifetime"
	case chatarchivecmd.StatusEnded:
		status = "Archived"
	case chatarchivecmd.StatusPending:
		status = "Activation pending"
	case chatarchivecmd.StatusUncertain:
		status = "Lifecycle uncertain"
	}
	person := "Multiple or unknown speakers"
	if s.Location.Audience == chatarchivecmd.AudiencePersonal {
		person = archiveSpeaker(summary.Speaker)
	}
	label := s.Label
	if label == "" {
		label = s.Location.Title
	}
	if label == "" {
		label = "Unnamed session"
	}
	return ChatSessionRow{Label: label, Channel: knownArchiveLabel(s.Location.Channel), Locator: knownArchiveLabel(s.Location.Locator), Title: knownArchiveLabel(s.Location.Title), Audience: knownArchiveLabel(s.Location.Audience), Subdivision: knownArchiveLabel(s.Location.Subdivision), SubdivisionLabel: s.Location.SubdivisionLabel, Person: person, Status: status, EndReason: s.EndReason, Started: archiveTimeLabel(s.StartedAt), Activity: archiveTimeLabel(activity), Ended: archiveTimeLabel(s.EndedAt), CoverageStart: archiveTimeLabel(s.CoverageStart), DetailPath: "/chat-sessions/" + url.PathEscape(s.ID) + "?" + query, MessageCount: summary.MessageCount, Partial: s.Partial}
}
func archiveQuery(q chatarchivecmd.BrowseQuery) url.Values {
	v := url.Values{}
	if q.Query != "" {
		v.Set("q", q.Query)
	}
	if q.Channel != "" {
		v.Set("channel", q.Channel)
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	return v
}

// ProjectChatInventory keeps filter navigation separate from raw storage records.
func ProjectChatInventory(inv chatarchivecmd.Inventory, q chatarchivecmd.BrowseQuery) *ChatArchiveView {
	values := archiveQuery(q)
	view := &ChatArchiveView{Query: q.Query, Channel: q.Channel, Status: q.Status, BackPath: "/chat-sessions?" + values.Encode()}
	detailValues := archiveQuery(q)
	if q.Cursor != "" {
		detailValues.Set("list_cursor", q.Cursor)
	}
	for _, row := range inv.Rows {
		view.Rows = append(view.Rows, archiveRow(row, detailValues.Encode()))
	}
	if inv.Older != "" {
		values.Set("cursor", inv.Older)
		view.OlderPath = "/chat-sessions?" + values.Encode()
	}
	return view
}

// ProjectChatTranscript copies an explicit allowlist, never private replay or source fields.
func ProjectChatTranscript(transcript chatarchivecmd.Transcript, q chatarchivecmd.BrowseQuery) *ChatArchiveView {
	values := archiveQuery(q)
	backValues := archiveQuery(q)
	if q.ListCursor != "" {
		backValues.Set("cursor", q.ListCursor)
		values.Set("list_cursor", q.ListCursor)
	}
	view := &ChatArchiveView{BackPath: "/chat-sessions?" + backValues.Encode()}
	row := archiveRow(transcript.Summary, values.Encode())
	view.Detail = &row
	if previous := transcript.Summary.Session.PredecessorID; previous != "" {
		view.PreviousPath = "/chat-sessions/" + url.PathEscape(previous) + "?" + values.Encode()
	}
	if next := transcript.SuccessorID; next != "" {
		view.NextPath = "/chat-sessions/" + url.PathEscape(next) + "?" + values.Encode()
	}
	if transcript.Older != "" {
		values.Set("cursor", transcript.Older)
		view.OlderPath = "/chat-sessions/" + url.PathEscape(transcript.Summary.Session.ID) + "?" + values.Encode()
	}
	for _, m := range transcript.Messages {
		if m.Role != archiveUserRole && m.Role != "agent" {
			continue
		}
		speaker := "Balda"
		if m.Role == archiveUserRole {
			speaker = archiveSpeaker(m.Speaker)
		}
		message := ChatMessageRow{Role: m.Role, Speaker: speaker, Principal: m.Speaker.Principal, Username: m.Speaker.Username, At: archiveTimeLabel(m.ObservedAt), Text: m.Text}
		for _, attachment := range m.Attachments {
			message.Attachments = append(message.Attachments, ChatAttachment{Name: attachment.Name, Kind: attachment.Kind})
		}
		view.Messages = append(view.Messages, message)
	}
	return view
}
