// Package skill owns the transport-neutral skill command policy.
package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

var (
	// ErrNotFound indicates that the selected skill is absent from the session snapshot.
	ErrNotFound = errors.New("skill not found")
	// ErrAmbiguous indicates that an unqualified skill name has multiple matches.
	ErrAmbiguous = errors.New("skill name is ambiguous")
	// ErrRevisionUnavailable indicates that the pinned skill revision cannot be restored.
	ErrRevisionUnavailable = errors.New("skill revision is unavailable")
	// ErrRuntimeUnavailable indicates that the skill execution adapter is unavailable.
	ErrRuntimeUnavailable = errors.New("skill runtime is unavailable")
)

const (
	commandName = "skill"

	msgDenied              = "Only users with session access can use this command."
	msgNotFound            = "Skill not found."
	msgAmbiguous           = "Skill name is ambiguous. Use a plugin-qualified name when selecting a plugin skill."
	msgRevisionUnavailable = "Skill is unavailable for this session. Reset the session and try again."
	msgRuntimeUnavailable  = "Skill runtime is unavailable right now. Please try again."
)

// Selector identifies either one uniquely named skill or one exact plugin skill.
type Selector struct {
	Plugin string
	Name   string
}

// Executor resolves a selector in trusted session scope and publishes its turn.
type Executor interface {
	ExecuteSkill(ctx context.Context, parent actorlayer.Envelope, payload commandcmd.Payload, selector Selector, prompt string) error
}

// Handler applies access, syntax, and bounded error policy for /skill.
type Handler struct {
	executor   Executor
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

// New creates a skill command handler.
func New(executor Executor, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Handler {
	return &Handler{
		executor:   executor,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.skill").Logger(),
	}
}

// Name returns the canonical command name.
func (h *Handler) Name() string { return commandName }

// Handle validates and executes one skill command invocation.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload) error {
	if !payload.Access.SessionCommands {
		return h.respond(ctx, env.ID, payload, msgDenied, "skill-denied")
	}

	selector, prompt, err := parseInvocation(payload.Args)
	if err != nil {
		return h.respond(ctx, env.ID, payload, usage(payload), "skill-usage")
	}
	if h.executor == nil {
		return h.respond(ctx, env.ID, payload, msgRuntimeUnavailable, "skill-unavailable")
	}

	err = h.executor.ExecuteSkill(ctx, env, payload, selector, prompt)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return h.respond(ctx, env.ID, payload, msgNotFound, "skill-not-found")
	case errors.Is(err, ErrAmbiguous):
		return h.respond(ctx, env.ID, payload, msgAmbiguous, "skill-ambiguous")
	case errors.Is(err, ErrRevisionUnavailable):
		return h.respond(ctx, env.ID, payload, msgRevisionUnavailable, "skill-revision-unavailable")
	case errors.Is(err, ErrRuntimeUnavailable):
		return h.respond(ctx, env.ID, payload, msgRuntimeUnavailable, "skill-unavailable")
	default:
		h.logger.Warn().Err(err).Str("session_id", payload.Locator.SessionID).Msg("failed to execute skill command")
		return actorlayer.TransientError(fmt.Errorf("execute skill command: %w", err))
	}
}

func (h *Handler) respond(ctx context.Context, operationID string, payload commandcmd.Payload, text, suffix string) error {
	if err := commandactor.SendPlain(ctx, h.dispatcher, operationID, payload.Locator, text, suffix); err != nil {
		return actorlayer.TransientError(fmt.Errorf("send skill command response: %w", err))
	}
	return nil
}

func parseInvocation(args string) (Selector, string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return Selector{}, "", errors.New("skill selector is required")
	}

	token, prompt := args, ""
	if i := strings.IndexFunc(args, unicode.IsSpace); i >= 0 {
		token = args[:i]
		prompt = strings.TrimSpace(args[i:])
	}

	parts := strings.Split(token, ":")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return Selector{}, "", errors.New("skill name is required")
		}
		return Selector{Name: parts[0]}, prompt, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return Selector{}, "", errors.New("plugin and skill names are required")
		}
		return Selector{Plugin: parts[0], Name: parts[1]}, prompt, nil
	default:
		return Selector{}, "", errors.New("skill selector has too many separators")
	}
}

func usage(payload commandcmd.Payload) string {
	root := strings.TrimSpace(payload.Invocation.Root)
	if root == "" || root == "/" {
		root = "/"
	} else if !strings.HasSuffix(root, " ") {
		root += " "
	}
	return "Usage:\n" + root + "skill <skill> [prompt...]\n" + root + "skill <plugin>:<skill> [prompt...]"
}
