package chatarchivefx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type questionArchive struct {
	inputs   *chatarchiveapp.Service
	sessions state.SessionStore
}

func (a *questionArchive) BindQuestion(ctx context.Context, interaction questioncmd.InteractionContext, resume questioncmd.ResumeTarget) (questioncmd.InteractionContext, error) {
	target, err := questioncmd.ParseResumeAddress(resume.To)
	if err != nil {
		return interaction, err
	}
	if target.Target != actorcmd.ActorTypeSession || interaction.Private {
		interaction.ArchiveID = ""
		return interaction, nil
	}
	if interaction.ArchiveID != "" {
		return interaction, nil
	}
	live, found, err := a.sessions.GetBySessionID(ctx, interaction.SessionID)
	if err != nil {
		return interaction, fmt.Errorf("bind question archive: %w", chatarchivecmd.ErrUnavailable)
	}
	if !found || live.ArchiveID == "" {
		return interaction, nil
	}
	interaction.ArchiveID = live.ArchiveID
	return interaction, nil
}

func (a *questionArchive) RecordAnswer(ctx context.Context, interaction questioncmd.InteractionContext, _ string, reply questioncmd.InboundReply) error {
	if interaction.ArchiveID == "" {
		return nil
	}
	if reply.OriginalInput == nil {
		return chatarchivecmd.ErrInvalid
	}
	input := *reply.OriginalInput.Clone()
	// Keep the transport identity shared with ordinary admission: a concurrent
	// answer can win settlement after this capture and send this reply there.
	if strings.TrimSpace(reply.MessageID) == "" {
		return chatarchivecmd.ErrInvalid
	}
	_, err := a.inputs.RecordInput(ctx, interaction.ArchiveID, input)
	return err
}
