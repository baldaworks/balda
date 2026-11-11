package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// ChatArchiveOperations is the SQL-only archive read port consumed by Backoffice.
type ChatArchiveOperations interface {
	Inventory(ctx context.Context, query chatarchivecmd.BrowseQuery, authority chatarchivecmd.Authority) (chatarchivecmd.Inventory, error)
	Transcript(ctx context.Context, id, cursor string, authority chatarchivecmd.Authority) (chatarchivecmd.Transcript, error)
}

// ConfigureChatArchiveOperations binds passive reads before serving HTTP.
func (r *Runtime) ConfigureChatArchiveOperations(operations ChatArchiveOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("chat archive operations must be configured before Backoffice start")
	}
	r.chatArchive = operations
	return nil
}

func (a *httpApp) chatArchivePage(w http.ResponseWriter, r *http.Request) {
	p, _ := security.PrincipalFromContext(r.Context())
	page := webui.Page{Title: "Chat sessions · Balda", ViewerUsername: p.User.Username, CSRFToken: a.browser.CSRFToken(r), Current: webui.LocationChatArchive, Navigation: webui.Navigation(users.BackofficeCapabilities(p.User), webui.LocationChatArchive)}
	authority := chatarchivecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, SessionID: p.FamilyID, SessionVersion: p.Version, At: time.Now().UTC()}
	query := chatarchivecmd.BrowseQuery{Query: r.URL.Query().Get("q"), Channel: r.URL.Query().Get("channel"), Status: r.URL.Query().Get("status"), Cursor: r.URL.Query().Get("cursor"), ListCursor: r.URL.Query().Get("list_cursor")}
	var err error
	switch {
	case a.chatArchive == nil:
		err = chatarchivecmd.ErrUnavailable
	case r.PathValue("archive_id") != "":
		var transcript chatarchivecmd.Transcript
		transcript, err = a.chatArchive.Transcript(r.Context(), r.PathValue("archive_id"), query.Cursor, authority)
		if err == nil {
			page.ChatArchive = webui.ProjectChatTranscript(transcript, query)
			page.Title = "Session chat · Balda"
		}
	default:
		var inventory chatarchivecmd.Inventory
		inventory, err = a.chatArchive.Inventory(r.Context(), query, authority)
		if err == nil {
			page.ChatArchive = webui.ProjectChatInventory(inventory, query)
		}
	}
	if err == nil {
		a.render(w, r, http.StatusOK, webui.TemplateChatArchive, page)
		return
	}
	status, message := http.StatusServiceUnavailable, "Chat archive is temporarily unavailable. Reopen Chat sessions to retry."
	switch {
	case errors.Is(err, chatarchivecmd.ErrForbidden):
		status, message = http.StatusForbidden, "Your administrator authority changed. Sign in again."
	case errors.Is(err, chatarchivecmd.ErrInvalid):
		status, message = http.StatusBadRequest, "The session address, filters or pagination cursor are invalid. Open Chat sessions to start again."
	case errors.Is(err, chatarchivecmd.ErrNotFound):
		status, message = http.StatusNotFound, "This archived session was not found. Open Chat sessions."
	}
	page.Error = &webui.ErrorView{Heading: "Chat archive could not be opened", Message: message}
	page.RestartURL, page.RestartLabel = a.path("/chat-sessions"), "Open Chat sessions"
	a.render(w, r, status, webui.TemplateError, page)
}
