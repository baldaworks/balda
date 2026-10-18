package backoffice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatArchivePreviews(t *testing.T) {
	for _, base := range []string{"", "/balda/backoffice"} {
		handler, err := QAHandler(base)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range []struct {
			path, want string
			status     int
		}{
			{"chat-sessions", "Before reset", 200}, {"chat-sessions?cursor=synthetic-older", "Older lifetime", 200},
			{"chat-sessions?cursor=synthetic-older&q=Before&channel=telegram&status=ended", "list_cursor=synthetic-older", 200},
			{"chat-sessions?Q=ignored&q=missing", "No chat sessions", 200}, {"chat-sessions-empty", "No chat sessions", 200},
			{"chat-sessions/before", "Next session", 200}, {"chat-sessions/after", "Previous session", 200},
			{"chat-sessions/closed", "close", 200}, {"chat-sessions/group", "Synthetic speaker two", 200},
			{"chat-sessions/partial", "Partial coverage", 200}, {"chat-sessions/pending", "Session activation is pending", 200},
			{"chat-sessions/uncertain", "Session status is uncertain", 200}, {"chat-sessions/unknown", "Unknown", 200},
			{"chat-sessions/empty", "No chat messages", 200}, {"chat-sessions/long", "&lt;script&gt;", 200},
			{"chat-sessions-invalid", "Invalid archive request", 400}, {"chat-sessions-forbidden", "Access denied", 403},
			{"chat-sessions-missing", "Session not found", 404}, {"chat-sessions-unavailable", "Archive unavailable", 503},
		} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/qa/ui/"+check.path, nil))
			if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) {
				t.Errorf("%s: status %d, expected %d and %q", check.path, response.Code, check.status, check.want)
			}
			denied := httptest.NewRecorder()
			handler.ServeHTTP(denied, httptest.NewRequest(http.MethodPost, base+"/qa/ui/"+check.path, nil))
			if denied.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s POST status %d", check.path, denied.Code)
			}
		}
	}
}
