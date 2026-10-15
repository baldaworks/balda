package commandcmd

import (
	"strings"
	"sync"
)

// Registry holds the current process-local dynamic command aliases by
// transport. Durable execution still resolves through the pinned catalog.
type Registry struct {
	mu       sync.RWMutex
	commands map[string]map[string]struct{}
	builtins map[string]map[string]struct{}
}

// NewRegistry creates an empty dynamic alias registry.
func NewRegistry() *Registry {
	return &Registry{commands: make(map[string]map[string]struct{}), builtins: make(map[string]map[string]struct{})}
}

// NewRegistryWithAdvertisements creates the registry from the command surface
// declared by commandfx transport advertisements. Concrete transports consult
// this registry instead of maintaining their own command whitelists.
func NewRegistryWithAdvertisements(advertisements []Advertisement) *Registry {
	registry := NewRegistry()
	for _, advertisement := range advertisements {
		if !advertisement.Enabled {
			continue
		}
		transport := strings.ToLower(strings.TrimSpace(advertisement.Transport))
		if transport == "" {
			continue
		}
		names := make(map[string]struct{}, len(advertisement.Names))
		for _, command := range advertisement.Names {
			if command = strings.ToLower(strings.TrimSpace(command)); command != "" {
				names[command] = struct{}{}
			}
		}
		registry.builtins[transport] = names
	}
	return registry
}

// Replace atomically replaces one transport's dynamic aliases.
func (r *Registry) Replace(transport string, projection AdvertisementProjection) {
	if r == nil {
		return
	}
	names := make(map[string]struct{}, len(projection.Commands))
	for _, command := range projection.Commands {
		if name := strings.ToLower(strings.TrimSpace(command.Name)); name != "" {
			names[name] = struct{}{}
		}
	}
	r.mu.Lock()
	r.commands[strings.ToLower(strings.TrimSpace(transport))] = names
	r.mu.Unlock()
}

// Supports reports whether a projected dynamic alias is currently active.
func (r *Registry) Supports(transport, name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	transport = strings.ToLower(strings.TrimSpace(transport))
	name = strings.ToLower(strings.TrimSpace(name))
	_, ok := r.builtins[transport][name]
	if !ok {
		_, ok = r.commands[transport][name]
	}
	r.mu.RUnlock()
	return ok
}
