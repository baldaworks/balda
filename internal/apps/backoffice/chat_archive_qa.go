package backoffice

import (
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

const qaChatBefore = "before"
const qaChatMissing = "missing"
const qaChatPending = "pending"

// These fixtures use deliberately synthetic identifiers, never live archive reads.
func qaChatArchiveEntries() []qaEntry {
	entries := []qaEntry{{name: "chat-sessions", label: "Chat sessions · inventory and pagination", templateName: webui.TemplateChatArchive, page: func() webui.Page { return qaChatInventory(chatarchivecmd.BrowseQuery{}) }, gallery: true}}
	for _, state := range []string{qaChatBefore, "after", "closed", "group", "partial", qaChatPending, "uncertain", "unknown", "empty", "long"} {
		entries = append(entries, qaEntry{name: "chat-sessions/" + state, label: "Chat sessions · " + state, templateName: webui.TemplateChatArchive, page: func() webui.Page { return qaChatTranscript(state, chatarchivecmd.BrowseQuery{}) }, gallery: true})
	}
	entries = append(entries, qaEntry{name: "chat-sessions-empty", label: "Chat sessions · empty inventory", templateName: webui.TemplateChatArchive, page: func() webui.Page { return qaChatInventory(chatarchivecmd.BrowseQuery{Query: qaChatMissing}) }, gallery: true})
	for _, item := range []struct {
		name, heading string
		status        int
	}{
		{"invalid", "Invalid archive request", http.StatusBadRequest}, {"forbidden", "Access denied", http.StatusForbidden},
		{qaChatMissing, "Session not found", http.StatusNotFound}, {"unavailable", "Archive unavailable", http.StatusServiceUnavailable},
	} {
		entries = append(entries, qaEntry{name: "chat-sessions-" + item.name, label: "Chat sessions · " + item.name, templateName: webui.TemplateError, status: item.status, gallery: true, page: func() webui.Page {
			p := qaChatPage()
			p.Error = &webui.ErrorView{Heading: item.heading, Message: "Synthetic archive preview. No live state was accessed."}
			return p
		}})
	}
	return entries
}

func qaChatPage() webui.Page {
	return webui.Page{Title: "Chat sessions · QA", Current: webui.LocationChatArchive, Navigation: qaAdminNavigation(webui.LocationChatArchive)}
}

func qaChatSummary(state string) chatarchivecmd.Summary {
	s := chatarchivecmd.Session{ID: state, Label: "Synthetic " + state, Status: chatarchivecmd.StatusActive, StartedAt: qaNow.Add(-time.Hour), ObservedAt: qaNow, Location: chatarchivecmd.Location{Channel: "telegram", Locator: "telegram:synthetic-invalid:topic", Title: "Synthetic personal chat", Audience: chatarchivecmd.AudiencePersonal, Subdivision: chatarchivecmd.SubdivisionTopic, SubdivisionLabel: "Synthetic topic"}}
	switch state {
	case qaChatBefore, "closed":
		s.Label = "Before reset"
		s.Status = chatarchivecmd.StatusEnded
		s.EndReason = "reset"
		s.EndedAt = qaNow
		if state == "closed" {
			s.Label = "After close"
			s.EndReason = "close"
		}
	case "after":
		s.Label = "After reset"
		s.PredecessorID = qaChatBefore
	case "group":
		s.Location.Channel = "slackagent"
		s.Location.Audience = chatarchivecmd.AudienceGroup
		s.Location.Subdivision = chatarchivecmd.SubdivisionThread
		s.Location.SubdivisionLabel = "Synthetic thread"
		s.Location.Title = "Synthetic group"
		s.Location.Locator = "slackagent:synthetic-invalid:thread"
	case "partial":
		s.Partial = true
		s.CoverageStart = qaNow.Add(-5 * time.Minute)
	case qaChatPending:
		s.Status = chatarchivecmd.StatusPending
	case "uncertain":
		s.Status = chatarchivecmd.StatusUncertain
	case "unknown":
		s.Location = chatarchivecmd.Location{}
	case "long":
		s.Label = strings.Repeat("SyntheticLongName", 16) + " <script>window.syntheticArchiveExecuted=true</script>"
		s.Location.Title = s.Label
		s.Location.Locator = "telegram:" + strings.Repeat("invalid-locator-", 30)
	case "older":
		s.Label = "Older lifetime"
		s.Status = chatarchivecmd.StatusEnded
	}
	count := int64(3)
	if state == "empty" || state == qaChatPending {
		count = 0
	}
	return chatarchivecmd.Summary{Session: s, Speaker: chatarchivecmd.Speaker{Name: "Synthetic speaker one"}, MessageCount: count}
}

func qaChatInventory(q chatarchivecmd.BrowseQuery) webui.Page {
	inv := chatarchivecmd.Inventory{Older: "synthetic-older"}
	for _, state := range []string{"after", qaChatBefore, "closed", "group", "partial", qaChatPending, "uncertain", "unknown", "long"} {
		inv.Rows = append(inv.Rows, qaChatSummary(state))
	}
	if q.Cursor != "" {
		inv.Rows = []chatarchivecmd.Summary{qaChatSummary("older")}
		inv.Older = ""
	}
	if q.Query == qaChatMissing {
		inv.Rows = nil
		inv.Older = ""
	}
	p := qaChatPage()
	p.ChatArchive = webui.ProjectChatInventory(inv, q)
	return p
}

func qaChatTranscript(state string, q chatarchivecmd.BrowseQuery) webui.Page {
	transcript := chatarchivecmd.Transcript{Summary: qaChatSummary(state)}
	if state == qaChatBefore {
		transcript.SuccessorID = "after"
	}
	if state != "empty" && state != qaChatPending {
		transcript.Messages = []chatarchivecmd.Message{
			{Role: "user", Speaker: chatarchivecmd.Speaker{Name: "Synthetic speaker one", Principal: "synthetic-person-one"}, Text: "Synthetic " + state + " original user message.\nSecond line remains readable.", ObservedAt: qaNow.Add(-2 * time.Minute)},
			{Role: "agent", Text: "Synthetic final agent answer.\nA separate session lifetime retains its own chat.", ObservedAt: qaNow.Add(-time.Minute)},
			{Role: "user", Speaker: chatarchivecmd.Speaker{Name: "Synthetic speaker two", Principal: "synthetic-person-two"}, Text: "Synthetic follow-up", Attachments: []chatarchivecmd.Attachment{{Name: "synthetic <report>.txt", Kind: "document"}}, ObservedAt: qaNow},
		}
	}
	if state == "long" {
		transcript.Messages[0].Text = "<script>window.syntheticArchiveExecuted=true</script>\n" + strings.Repeat("SyntheticUnbrokenChat", 40) + "\nПоследняя строка"
		transcript.Messages[2].Attachments[0].Name = strings.Repeat("SyntheticAttachment", 20) + ".txt"
	}
	if state == "older" {
		transcript.Older = "synthetic-messages"
	}
	if q.Cursor != "" {
		transcript.Messages = []chatarchivecmd.Message{{Role: "user", Text: "Older synthetic message", ObservedAt: qaNow.Add(-time.Hour)}}
		transcript.Older = ""
	}
	p := qaChatPage()
	p.ChatArchive = webui.ProjectChatTranscript(transcript, q)
	return p
}

func qaChatRequest(r *http.Request, name string) (webui.Page, bool) {
	q := chatarchivecmd.BrowseQuery{Query: r.URL.Query().Get("q"), Channel: r.URL.Query().Get("channel"), Status: r.URL.Query().Get("status"), Cursor: r.URL.Query().Get("cursor"), ListCursor: r.URL.Query().Get("list_cursor")}
	if name == "chat-sessions" {
		return qaChatInventory(q), true
	}
	if state, ok := strings.CutPrefix(name, "chat-sessions/"); ok {
		for _, known := range []string{qaChatBefore, "after", "closed", "group", "partial", qaChatPending, "uncertain", "unknown", "empty", "long", "older"} {
			if state == known {
				return qaChatTranscript(state, q), true
			}
		}
	}
	return webui.Page{}, false
}
