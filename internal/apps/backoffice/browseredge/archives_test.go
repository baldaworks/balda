package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestArchiveBrowserEscapesTranscriptAndPreservesNavigation(t *testing.T) {
	id := uuid.New()
	service := &archiveEdgeService{transcript: managementv1.AISessionArchiveTranscript{
		Summary:     managementv1.AISessionArchiveSummary{AISessionID: id.String(), Channel: "telegram", Status: "ended", Speaker: managementv1.AISessionArchiveSpeaker{}},
		Messages:    []managementv1.AISessionArchiveMessage{{Sequence: 1, Role: managementv1.User, Text: `<script>private()</script>`, Speaker: managementv1.AISessionArchiveSpeaker{Name: "Alice"}, Attachments: []managementv1.AISessionArchiveAttachment{{Name: `<img>`, Kind: "image"}}}},
		OlderCursor: "opaque", SuccessorAISessionID: uuid.NewString(),
	}}
	app := newTestApp(t, service)
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/chat-sessions/"+id.String()+"?q=alpha&list_cursor=list-token", nil)
	request.Header.Set("Cookie", accessCookie+"=access")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `&lt;script&gt;private()&lt;/script&gt;`)
	require.Contains(t, response.Body.String(), "list-token")
	require.NotContains(t, response.Body.String(), "<script>")
}

type archiveEdgeService struct {
	fakeService
	inventory  managementv1.AISessionArchiveInventory
	transcript managementv1.AISessionArchiveTranscript
}

func (*archiveEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *archiveEdgeService) AISessionArchives(context.Context, managementclient.Auth, *managementv1.ListAISessionArchivesParams) (managementv1.AISessionArchiveInventory, error) {
	return s.inventory, nil
}
func (s *archiveEdgeService) AISessionArchive(context.Context, managementclient.Auth, string, *managementv1.GetAISessionArchiveParams) (managementv1.AISessionArchiveTranscript, error) {
	return s.transcript, nil
}

var _ Service = (*archiveEdgeService)(nil)
var _ ArchiveService = (*archiveEdgeService)(nil)
