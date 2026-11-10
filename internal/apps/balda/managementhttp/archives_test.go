package managementhttp

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestArchiveHandlersExposePassiveAllowlist(t *testing.T) {
	id := uuid.NewString()
	fixture := &archiveFixture{inventory: chatarchivecmd.Inventory{Rows: []chatarchivecmd.Summary{{Session: chatarchivecmd.Session{ID: id, Label: "safe", Status: chatarchivecmd.StatusEnded}, MessageCount: 2}}, Older: "older"},
		transcript: chatarchivecmd.Transcript{Summary: chatarchivecmd.Summary{Session: chatarchivecmd.Session{ID: id}}, Messages: []chatarchivecmd.Message{
			{Sequence: 1, Role: "user", Text: "visible", Source: "private-source", Replay: []byte("private-replay"), FinalMembers: []string{"private-member"}},
			{Sequence: 2, Role: "tool", Text: "private-tool"},
		}, Older: "messages-older"}}
	server := strictServer{archives: fixture}

	listed, err := server.ListAISessionArchives(t.Context(), managementv1.ListAISessionArchivesRequestObject{})
	require.NoError(t, err)
	inventory := listed.(managementv1.ListAISessionArchives200JSONResponse)
	require.Equal(t, "older", inventory.OlderCursor)
	require.Equal(t, id, inventory.Rows[0].AISessionID)

	result, err := server.GetAISessionArchive(t.Context(), managementv1.GetAISessionArchiveRequestObject{AISessionID: id})
	require.NoError(t, err)
	transcript := result.(managementv1.GetAISessionArchive200JSONResponse)
	require.Len(t, transcript.Messages, 1)
	require.Equal(t, "visible", transcript.Messages[0].Text)
	require.NotContains(t, transcript.Messages[0].Text, "private")
	require.Equal(t, 2, fixture.readCalls)
}

type archiveFixture struct {
	inventory  chatarchivecmd.Inventory
	transcript chatarchivecmd.Transcript
	readCalls  int
}

func (f *archiveFixture) Inventory(context.Context, chatarchivecmd.BrowseQuery, chatarchivecmd.Authority) (chatarchivecmd.Inventory, error) {
	f.readCalls++
	return f.inventory, nil
}
func (f *archiveFixture) Transcript(context.Context, string, string, chatarchivecmd.Authority) (chatarchivecmd.Transcript, error) {
	f.readCalls++
	return f.transcript, nil
}

var _ archiveOperations = (*archiveFixture)(nil)
