package chatarchivefx

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
)

type skillAdmission struct {
	inputs   *chatarchiveapp.Service
	sessions *session.Manager
}

func (a *skillAdmission) AdmitSkillInput(ctx context.Context, payload commandcmd.Payload) (string, error) {
	if payload.OriginalInput == nil {
		return "", actorlayer.PolicyError(chatarchivecmd.ErrInvalid)
	}
	topic, err := a.sessions.EnsureSession(ctx, session.SessionContext{Locator: payload.Locator, UserID: payload.Principal}, "auto")
	if err != nil {
		return "", err
	}
	recorded, err := a.inputs.RecordInput(ctx, topic.GetArchiveID(), *payload.OriginalInput)
	if err != nil {
		return "", actorlayer.TransientError(err)
	}
	if recorded.SessionID != topic.GetArchiveID() {
		return "", actorlayer.PolicyError(fmt.Errorf("skill input lifetime: %w", chatarchivecmd.ErrConflict))
	}
	return recorded.SessionID, nil
}
