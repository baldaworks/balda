package handlers

import (
	"context"
	"fmt"
	"strings"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

type userHandler struct {
	ownerStore        *auth.OwnerStore
	inviteStore       *auth.InviteStore
	collaboratorStore *auth.CollaboratorStore
	actorDispatcher   actortransport.Dispatcher
	tgClient          client.ClientWithResponsesInterface
	botUsername       string
}

type userHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	InviteStore       *auth.InviteStore
	CollaboratorStore *auth.CollaboratorStore
	Dispatcher        actortransport.Dispatcher
	TGClient          client.ClientWithResponsesInterface `optional:"true"`
}

func (h *userHandler) getBotUsername(ctx context.Context) string {
	if h.botUsername != "" {
		return h.botUsername
	}
	if h.tgClient == nil {
		return ""
	}
	resp, err := h.tgClient.GetMeWithResponse(ctx)
	if err != nil {
		return ""
	}
	if resp.JSON200 == nil || resp.JSON200.Result.Username == nil {
		return ""
	}
	h.botUsername = *resp.JSON200.Result.Username
	return h.botUsername
}

func (h *userHandler) HandleUserCommand(ctx context.Context, req commandapp.Request) error {
	if !h.ownerStore.IsOwner(req.UserID) {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "This command is only for the owner."); err != nil {
			return err
		}
		return nil
	}

	args := strings.Fields(req.Args)
	if len(args) == 0 {
		return h.sendUsage(ctx, req.Locator)
	}

	switch args[0] {
	case userActionAdd:
		return h.onAdd(ctx, req)
	case userActionList:
		return h.onList(ctx, req)
	case userActionRemove:
		return h.onRemove(ctx, req)
	default:
		return h.sendUsage(ctx, req.Locator)
	}
}

func (h *userHandler) sendUsage(ctx context.Context, locator baldasession.SessionLocator) error {
	usage := "Usage:\n" +
		"• /user add - Generate invite link\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"
	return sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, locator, usage)
}

func (h *userHandler) onAdd(ctx context.Context, req commandapp.Request) error {
	ownerID := fmt.Sprintf("%d", req.UserID)

	token, _, err := h.inviteStore.CreateInvite(ctx, ownerID)
	if err != nil {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "Failed to create invite. Please try again."); err != nil {
			return err
		}
		return nil
	}

	username := strings.TrimSpace(h.getBotUsername(ctx))
	if username == "" {
		username = "<bot_username>"
	}
	inviteLink := fmt.Sprintf("https://t.me/%s?start=invite_%s", username, token)
	message := fmt.Sprintf("Invite link created:\n%s\n\nVisit this link to become a bot collaborator", inviteLink)

	if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, message); err != nil {
		return err
	}
	return nil
}

func (h *userHandler) onList(ctx context.Context, req commandapp.Request) error {
	var lines []string

	collaborators, err := h.collaboratorStore.ListCollaborators(ctx)
	if err != nil {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "Failed to list collaborators. Please try again."); err != nil {
			return err
		}
		return nil
	}

	if len(collaborators) > 0 {
		lines = append(lines, "Collaborators:")
		for _, c := range collaborators {
			name := "unknown"
			if strings.TrimSpace(c.Username) != "" {
				name = "@" + c.Username
			} else if strings.TrimSpace(c.FirstName) != "" {
				name = c.FirstName
			}
			lines = append(lines, fmt.Sprintf("• %s (%s) - added %s",
				c.UserID, name, c.AddedAt.Format("2006-01-02 15:04")))
		}
	} else {
		lines = append(lines, "No collaborators")
	}

	invites, err := h.inviteStore.ListInvites(ctx)
	if err != nil {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "Failed to list invites. Please try again."); err != nil {
			return err
		}
		return nil
	}

	if len(invites) > 0 {
		lines = append(lines, "", "Active Invites:")
		for _, inv := range invites {
			lines = append(lines, fmt.Sprintf("expires %s", inv.ExpiresAt.Format("2006-01-02 15:04")))
		}
	}

	message := strings.Join(lines, "\n")
	if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, message); err != nil {
		return err
	}
	return nil
}

func (h *userHandler) onRemove(ctx context.Context, req commandapp.Request) error {
	args := strings.Fields(req.Args)
	if len(args) < 2 {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "Usage: /user remove <user_id>"); err != nil {
			return err
		}
		return nil
	}

	userID := strings.TrimSpace(args[1])
	if userID == "" {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "User ID required"); err != nil {
			return err
		}
		return nil
	}

	if err := h.collaboratorStore.RemoveCollaborator(ctx, userID); err != nil {
		if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, "Could not remove collaborator. Please try again."); err != nil {
			return err
		}
		return nil
	}

	message := fmt.Sprintf("Collaborator removed: %s", userID)
	if err := sendPlain(ctx, h.actorDispatcher, userHandlerActorAddress, req.Locator, message); err != nil {
		return err
	}
	return nil
}
