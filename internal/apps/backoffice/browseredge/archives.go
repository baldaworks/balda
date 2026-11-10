package browseredge

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (a *App) archivePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ArchiveService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	values := r.URL.Query()
	params := &managementv1.ListAISessionArchivesParams{}
	if value := values.Get("q"); value != "" {
		params.Q = &value
	}
	if value := values.Get("channel"); value != "" {
		params.Channel = &value
	}
	if value := values.Get("status"); value != "" {
		status := managementv1.ListAISessionArchivesParamsStatus(value)
		params.Status = &status
	}
	if value := values.Get("cursor"); value != "" {
		params.Cursor = &value
	}
	inventory, err := service.AISessionArchives(r.Context(), a.auth(r), params)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p><a href="` + a.path("/overview") + `">Overview</a></p><form method="get" action="` + a.path("/chat-sessions") + `"><label>Search <input name="q" maxlength="200" value="` + escape(values.Get("q")) + `"></label><label>Channel <input name="channel" maxlength="40" value="` + escape(values.Get("channel")) + `"></label><label>Status <input name="status" value="` + escape(values.Get("status")) + `"></label><button>Filter</button></form><ul>`
	listValues := archiveFilterValues(values)
	if cursor := values.Get("cursor"); cursor != "" {
		listValues.Set("list_cursor", cursor)
	}
	for _, row := range inventory.Rows {
		label := row.Label
		if label == "" {
			label = row.Title
		}
		if label == "" {
			label = "Unnamed session"
		}
		body += `<li><a href="` + a.path("/chat-sessions/") + url.PathEscape(row.AISessionID) + `?` + escape(listValues.Encode()) + `">` + escape(label) + `</a> · ` + escape(row.Channel) + ` · ` + escape(row.Status) + ` · messages: ` + strconv.FormatInt(row.MessageCount, 10) + `</li>`
	}
	if len(inventory.Rows) == 0 {
		body += `<li>No chat sessions.</li>`
	}
	body += htmlListEnd
	if inventory.OlderCursor != "" {
		older := archiveFilterValues(values)
		older.Set("cursor", inventory.OlderCursor)
		body += `<p><a href="` + a.path("/chat-sessions?") + escape(older.Encode()) + `">Older sessions</a></p>`
	}
	a.page(w, http.StatusOK, "AI Session archives", body)
}

func (a *App) archiveTranscriptPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	service, ok := a.service.(ArchiveService)
	if !ok {
		a.failure(w, http.StatusServiceUnavailable)
		return
	}
	params := &managementv1.GetAISessionArchiveParams{}
	if value := r.URL.Query().Get("cursor"); value != "" {
		params.Cursor = &value
	}
	transcript, err := service.AISessionArchive(r.Context(), a.auth(r), r.PathValue("ai_session_id"), params)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	back := archiveFilterValues(r.URL.Query())
	if listCursor := r.URL.Query().Get("list_cursor"); listCursor != "" {
		back.Set("cursor", listCursor)
	}
	body := `<p><a href="` + a.path("/chat-sessions?") + escape(back.Encode()) + `">Back to AI Session archives</a></p>`
	summary := transcript.Summary
	body += `<dl><dt>AI Session ID</dt><dd>` + escape(summary.AISessionID) + `</dd><dt>Channel</dt><dd>` + escape(summary.Channel) + `</dd><dt>Location</dt><dd>` + escape(summary.Title) + `</dd><dt>Status</dt><dd>` + escape(summary.Status) + `</dd></dl>`
	navigation := archiveFilterValues(r.URL.Query())
	if summary.PredecessorAISessionID != "" {
		body += `<a href="` + a.path("/chat-sessions/") + url.PathEscape(summary.PredecessorAISessionID) + `?` + escape(navigation.Encode()) + `">Previous session</a> `
	}
	if transcript.SuccessorAISessionID != "" {
		body += `<a href="` + a.path("/chat-sessions/") + url.PathEscape(transcript.SuccessorAISessionID) + `?` + escape(navigation.Encode()) + `">Next session</a>`
	}
	if transcript.OlderCursor != "" {
		navigation.Set("cursor", transcript.OlderCursor)
		body += `<p><a href="` + a.path("/chat-sessions/") + url.PathEscape(summary.AISessionID) + `?` + escape(navigation.Encode()) + `">Older messages</a></p>`
	}
	for _, message := range transcript.Messages {
		speaker := message.Speaker.Name
		if speaker == "" {
			speaker = message.Speaker.Username
		}
		if speaker == "" {
			speaker = message.Speaker.Principal
		}
		if message.Role == managementv1.Agent {
			speaker = "Balda"
		}
		body += `<article><h2>` + escape(speaker) + ` · ` + escape(string(message.Role)) + `</h2><time>` + escape(archiveTime(message.ObservedAt)) + `</time><pre>` + escape(message.Text) + `</pre>`
		for _, attachment := range message.Attachments {
			body += `<p>Attachment: ` + escape(attachment.Name) + ` (` + escape(attachment.Kind) + `)</p>`
		}
		body += `</article>`
	}
	if len(transcript.Messages) == 0 {
		body += `<p>No chat messages.</p>`
	}
	a.page(w, http.StatusOK, "AI Session transcript", body)
}

func archiveFilterValues(source url.Values) url.Values {
	values := url.Values{}
	for _, key := range []string{"q", "channel", "status", "list_cursor"} {
		if value := source.Get(key); value != "" {
			values.Set(key, value)
		}
	}
	return values
}

func archiveTime(value *time.Time) string {
	if value == nil {
		return "Unknown"
	}
	return value.UTC().Format(time.RFC3339)
}
