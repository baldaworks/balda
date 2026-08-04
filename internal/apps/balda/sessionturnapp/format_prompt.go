package sessionturnapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

const (
	stateKeyMessageFormatAnnounced = "balda_message_format_announced"
	stateKeyMessageFormatName      = "balda_message_format_name"
)

type messageFormatRegistry interface {
	Resolve(transport string, format deliveryfmt.DeliveryFormat) (deliveryfmt.Name, deliveryfmt.Format, deliveryfmt.Formatter, error)
}

type runtimeStateStore interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
	UpdateRuntimeState(ctx context.Context, locator baldasession.SessionLocator, state map[string]any) error
}

type formatStateChange struct {
	announced bool
	name      deliveryfmt.Name
}

// FormatPromptComposer adds registry-owned formatting guidance when the
// effective registered format changes for one runtime session.
type FormatPromptComposer struct {
	registry messageFormatRegistry
	state    runtimeStateStore
}

func NewFormatPromptComposer(registry messageFormatRegistry, state runtimeStateStore) *FormatPromptComposer {
	return &FormatPromptComposer{registry: registry, state: state}
}

func (c *FormatPromptComposer) Compose(
	ctx context.Context,
	locator baldasession.SessionLocator,
	format deliveryfmt.DeliveryFormat,
	userText string,
) (string, *formatStateChange, error) {
	if c == nil || c.state == nil {
		return "", nil, fmt.Errorf("message format runtime state is required")
	}
	announced, previousName, err := c.readState(ctx, locator)
	if err != nil {
		return "", nil, err
	}
	format = deliveryfmt.NormalizeDeliveryFormat(format)
	if format == "" {
		if !announced {
			return userText, nil, nil
		}
		return composeFormatResetPrompt(userText), &formatStateChange{}, nil
	}
	if c.registry == nil {
		return "", nil, fmt.Errorf("message format registry is required")
	}

	name, registered, _, err := c.registry.Resolve(locator.ChannelType, format)
	if err != nil {
		return "", nil, fmt.Errorf("resolve turn message format: %w", err)
	}
	if announced && previousName == name {
		return userText, nil, nil
	}
	return composeFormatPrompt(userText, registered), &formatStateChange{announced: true, name: name}, nil
}

func (c *FormatPromptComposer) Commit(
	ctx context.Context,
	locator baldasession.SessionLocator,
	change formatStateChange,
) error {
	if c == nil || c.state == nil {
		return fmt.Errorf("message format runtime state is required")
	}
	if err := c.state.UpdateRuntimeState(ctx, locator, map[string]any{
		stateKeyMessageFormatAnnounced: change.announced,
		stateKeyMessageFormatName:      string(change.name),
	}); err != nil {
		return fmt.Errorf("commit message format state: %w", err)
	}
	return nil
}

func (c *FormatPromptComposer) readState(
	ctx context.Context,
	locator baldasession.SessionLocator,
) (bool, deliveryfmt.Name, error) {
	announcedValue, announcedSet, err := c.state.RuntimeStateValue(ctx, locator, stateKeyMessageFormatAnnounced)
	if err != nil {
		return false, "", fmt.Errorf("read announced message format state: %w", err)
	}
	announced := false
	if announcedSet {
		var ok bool
		announced, ok = announcedValue.(bool)
		if !ok {
			return false, "", fmt.Errorf("message format state %q has type %T, want bool", stateKeyMessageFormatAnnounced, announcedValue)
		}
	}

	nameValue, nameSet, err := c.state.RuntimeStateValue(ctx, locator, stateKeyMessageFormatName)
	if err != nil {
		return false, "", fmt.Errorf("read message format name state: %w", err)
	}
	var name deliveryfmt.Name
	if nameSet {
		storedName, ok := nameValue.(string)
		if !ok {
			return false, "", fmt.Errorf("message format state %q has type %T, want string", stateKeyMessageFormatName, nameValue)
		}
		name = deliveryfmt.Name(strings.TrimSpace(storedName))
	}
	if announced && name == "" {
		return false, "", fmt.Errorf("announced message format has no registered name")
	}
	return announced, name, nil
}

func composeFormatPrompt(userText string, format deliveryfmt.Format) string {
	var prompt strings.Builder
	prompt.WriteString("[application-message-format]\n")
	fmt.Fprintf(&prompt, "name: %s\n", format.Name)
	prompt.WriteString(format.Instructions)
	prompt.WriteString("\nexample:\n")
	prompt.WriteString(format.Example)
	prompt.WriteString("\n[/application-message-format]\n\n")
	writeFormatUserRequest(&prompt, userText)
	return prompt.String()
}

func composeFormatResetPrompt(userText string) string {
	var prompt strings.Builder
	prompt.WriteString("[application-message-format]\n")
	prompt.WriteString("name: none\n")
	prompt.WriteString("Previous application message-format rules no longer apply.\n")
	prompt.WriteString("[/application-message-format]\n\n")
	writeFormatUserRequest(&prompt, userText)
	return prompt.String()
}

func writeFormatUserRequest(prompt *strings.Builder, userText string) {
	prompt.WriteString("[user-request]\n")
	prompt.WriteString(userText)
	prompt.WriteString("\n[/user-request]")
}
