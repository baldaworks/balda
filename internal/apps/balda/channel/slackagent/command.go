package slackagent

import (
	"errors"
	"fmt"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

const slackRootCommand = "/balda"

var errUnsupportedCommand = errors.New("unsupported /balda command")

var supportedCommands = []string{"start", "locator", "status", "reset", "skill"}

func SupportedCommands() []string { return append([]string(nil), supportedCommands...) }

func decodeCommandRequestWithRegistry(body []byte, registry *commandcmd.Registry) (commandcmd.Request, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return commandcmd.Request{}, fmt.Errorf("decode slack command form: %w", err)
	}
	if command := strings.TrimSpace(form.Get("command")); command != slackRootCommand {
		return commandcmd.Request{}, fmt.Errorf("unsupported slack command %q", command)
	}
	teamID := strings.TrimSpace(form.Get("team_id"))
	if teamID == "" {
		return commandcmd.Request{}, fmt.Errorf("slack command team_id is required")
	}
	conversationID := strings.TrimSpace(form.Get("channel_id"))
	if conversationID == "" {
		return commandcmd.Request{}, fmt.Errorf("slack command channel_id is required")
	}
	userID := strings.TrimSpace(form.Get("user_id"))
	if userID == "" {
		return commandcmd.Request{}, fmt.Errorf("slack command user_id is required")
	}

	locator := NewConversationLocator(teamID, conversationID)
	scope, err := ClassifyLocatorScope(locator)
	if err != nil {
		return commandcmd.Request{}, fmt.Errorf("classify slack command conversation: %w", err)
	}
	fields := strings.Fields(form.Get("text"))
	if len(fields) == 0 || (!commandSupported(strings.ToLower(fields[0])) && (registry == nil || !registry.Supports(ChannelType, fields[0]))) {
		return commandcmd.Request{}, errUnsupportedCommand
	}
	name := strings.ToLower(fields[0])
	args := strings.Join(fields[1:], " ")
	options := deliveryfmt.NormalizeOptions(deliveryfmt.Options{
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
		ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: true, PlanUpdates: true},
	})
	var original *chatarchivecmd.Input
	if name == "skill" {
		original = &chatarchivecmd.Input{Text: slackRootCommand + " " + form.Get("text"), ReceivedAt: time.Now().UTC(), Speaker: chatarchivecmd.Speaker{Principal: slackUserID(teamID, userID), Name: form.Get("user_name")}, Location: chatarchivecmd.Location{Channel: locator.ChannelType, AddressKey: locator.AddressKey, Locator: locatorref.Format(locator), Audience: chatarchivecmd.AudienceUnknown, Subdivision: chatarchivecmd.SubdivisionRoot}}
		if scope == deliverycmd.LocatorScopePersonal {
			original.Location.Audience = chatarchivecmd.AudiencePersonal
		}
	}
	return commandcmd.Request{Payload: commandcmd.Payload{
		OriginalInput: original,
		Version:       commandcmd.SchemaVersion,
		Name:          name,
		Args:          args,
		Locator:       locator,
		Transport:     ChannelType,
		Principal:     slackUserID(teamID, userID),
		Access:        commandcmd.Access{SessionCommands: true, WorkspaceMember: true},
		Conversation:  commandcmd.Conversation{Direct: scope == deliverycmd.LocatorScopePersonal},
		Presentation:  options,
		Invocation:    commandcmd.Invocation{Root: slackRootCommand},
	}}, nil
}

func commandUsage() string {
	return "Usage: " + slackRootCommand + " " + strings.Join(supportedCommands, " | ")
}

func commandSupported(name string) bool {
	for _, supported := range supportedCommands {
		if name == supported {
			return true
		}
	}
	return false
}
