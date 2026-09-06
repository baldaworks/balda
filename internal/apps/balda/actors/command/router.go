// Package command owns Balda product command routing and execution.
package command

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
)

// Handler executes one canonical command. Implementations own command policy.
type Handler interface {
	Name() string
	Handle(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload) error
}

// Router is an immutable canonical-name routing table.
type Router struct{ handlers map[string]Handler }

func NewRouter(handlers []Handler) (*Router, error) {
	r := &Router{handlers: make(map[string]Handler, len(handlers))}
	for _, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("nil command handler")
		}
		name := strings.ToLower(strings.TrimSpace(handler.Name()))
		if name == "" {
			return nil, fmt.Errorf("command handler name is required")
		}
		if _, exists := r.handlers[name]; exists {
			return nil, fmt.Errorf("duplicate command handler %q", name)
		}
		r.handlers[name] = handler
	}
	return r, nil
}

func (r *Router) Resolve(name string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	h, ok := r.handlers[strings.ToLower(strings.TrimSpace(name))]
	return h, ok
}

func (r *Router) ValidateAdvertised(names []string) error {
	missing := make([]string, 0)
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := r.handlers[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("advertised commands have no actor handler: %s", strings.Join(missing, ", "))
}
