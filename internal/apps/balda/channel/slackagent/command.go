package slackagent

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

const slackRootCommand = "/balda"

func decodeCommandRequest(body []byte) (commandapp.Request, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return commandapp.Request{}, fmt.Errorf("decode slack command form: %w", err)
	}
	if command := strings.TrimSpace(form.Get("command")); command != slackRootCommand {
		return commandapp.Request{}, fmt.Errorf("unsupported slack command %q", command)
	}
	teamID := strings.TrimSpace(form.Get("team_id"))
	if teamID == "" {
		return commandapp.Request{}, fmt.Errorf("slack command team_id is required")
	}
	conversationID := strings.TrimSpace(form.Get("channel_id"))
	if conversationID == "" {
		return commandapp.Request{}, fmt.Errorf("slack command channel_id is required")
	}
	userID := strings.TrimSpace(form.Get("user_id"))
	if userID == "" {
		return commandapp.Request{}, fmt.Errorf("slack command user_id is required")
	}

	locator := NewConversationLocator(teamID, conversationID)
	scope, err := ClassifyLocatorScope(locator)
	if err != nil {
		return commandapp.Request{}, fmt.Errorf("classify slack command conversation: %w", err)
	}
	fields := strings.Fields(form.Get("text"))
	command := ""
	args := ""
	if len(fields) > 0 {
		command = strings.ToLower(fields[0])
		args = strings.Join(fields[1:], " ")
	}
	options := deliveryfmt.NormalizeOptions(deliveryfmt.Options{
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: true, PlanUpdates: true},
	})
	return commandapp.Request{
		Locator:         locator,
		DeliveryOptions: options,
		Transport:       ChannelType,
		Subject:         slackUserID(teamID, userID),
		Access:          commandapp.Access{SessionCommands: true},
		Invocation:      commandapp.Invocation{Root: slackRootCommand},
		EnabledCommands: []string{"locator"},
		Command:         command,
		Args:            args,
		IsDM:            scope == deliverycmd.LocatorScopePersonal,
	}, nil
}
