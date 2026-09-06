// Package commandcmd defines the transport-neutral durable command contract.
package commandcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/go-actorlayer"
)

const (
	SchemaVersion = 1
)

// Access contains immutable capabilities established by ingress.
type Access struct {
	SessionCommands bool `json:"session_commands,omitempty"`
	Owner           bool `json:"owner,omitempty"`
	Collaborator    bool `json:"collaborator,omitempty"`
	WorkspaceMember bool `json:"workspace_member,omitempty"`
}

// Conversation describes command context without provider SDK values.
type Conversation struct {
	Direct bool `json:"direct,omitempty"`
}

// Invocation contains presentation-only syntax information.
type Invocation struct {
	Root string `json:"root"`
}

// Payload is the command body routed solely by Name.
type Payload struct {
	Version      int                 `json:"version"`
	Name         string              `json:"name"`
	Args         string              `json:"args,omitempty"`
	Locator      deliverycmd.Locator `json:"locator"`
	Transport    string              `json:"transport"`
	Principal    string              `json:"principal"`
	Access       Access              `json:"access"`
	Conversation Conversation        `json:"conversation"`
	Presentation deliveryfmt.Options `json:"presentation"`
	Invocation   Invocation          `json:"invocation"`
}

func (p Payload) Validate() error {
	if p.Version != SchemaVersion {
		return fmt.Errorf("unsupported command payload version %d", p.Version)
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("command name is required")
	}
	if strings.TrimSpace(p.Transport) == "" {
		return errors.New("command transport is required")
	}
	if strings.TrimSpace(p.Principal) == "" {
		return errors.New("command principal is required")
	}
	if strings.TrimSpace(p.Locator.ChannelType) == "" || strings.TrimSpace(p.Locator.AddressKey) == "" {
		return errors.New("command locator is required")
	}
	if !strings.EqualFold(strings.TrimSpace(p.Transport), strings.TrimSpace(p.Locator.ChannelType)) {
		return errors.New("command transport does not match locator")
	}
	if strings.TrimSpace(p.Locator.SessionID) == "" {
		return errors.New("command session id is required")
	}
	return nil
}

// EnvelopeOptions contains stable actor metadata supplied by ingress.
type EnvelopeOptions struct {
	ID, DedupeKey, CorrelationID, CausationID string
	From                                      actorlayer.ActorAddress
}

// Request is the authenticated, parsed ingress handoff before durable publication.
type Request struct {
	Payload      Payload
	InvocationID string
}

// Ingress durably accepts an authenticated transport command.
type Ingress interface {
	PublishCommand(ctx context.Context, request Request) error
}

// Advertisement exposes one transport's actor-routed whitelist to startup validation.
type Advertisement struct {
	Transport string
	Enabled   bool
	Names     []string
}

// NewEnvelope validates and serializes one durable command.
func NewEnvelope(p Payload, opts EnvelopeOptions) (actorlayer.Envelope, error) {
	if err := p.Validate(); err != nil {
		return actorlayer.Envelope{}, err
	}
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	env := actorlayer.Envelope{
		ID: strings.TrimSpace(opts.ID), Namespace: actorcmd.NamespaceChatCommand, Kind: actorcmd.KindCommandExecute,
		From: opts.From, To: actorlayer.ActorAddress{Target: actorcmd.ActorTypeCommand, Key: p.Locator.SessionID},
		CorrelationID: strings.TrimSpace(opts.CorrelationID), CausationID: strings.TrimSpace(opts.CausationID),
		DedupeKey: strings.TrimSpace(opts.DedupeKey),
	}
	if env.DedupeKey == "" {
		env.DedupeKey = env.ID
	}
	var err error
	env.Payload, err = actorlayer.MarshalPayload(p)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("marshal command: %w", err)
	}
	if err := env.Validate(); err != nil {
		return actorlayer.Envelope{}, err
	}
	return env, nil
}

// Decode validates command taxonomy and payload shape.
func Decode(env actorlayer.Envelope) (Payload, error) {
	if env.Namespace != actorcmd.NamespaceChatCommand || env.Kind != actorcmd.KindCommandExecute || !strings.EqualFold(env.To.Target, actorcmd.ActorTypeCommand) {
		return Payload{}, errors.New("invalid command envelope taxonomy")
	}
	var p Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &p); err != nil {
		return Payload{}, fmt.Errorf("decode command body: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Payload{}, err
	}
	if strings.TrimSpace(env.To.Key) != strings.TrimSpace(p.Locator.SessionID) {
		return Payload{}, errors.New("command actor key does not match locator session id")
	}
	return p, nil
}
