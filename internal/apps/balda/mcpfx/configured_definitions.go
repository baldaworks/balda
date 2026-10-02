package mcpfx

import (
	"context"
	"maps"
	"slices"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

// ConfiguredDefinitions contains only redacted inventory metadata, never
// another copy of the configured launch credentials.
type ConfiguredDefinitions struct {
	items     []mcpcmd.Item
	providers []string
}

// NewConfiguredDefinitions translates current Norma configuration for the UI.
func NewConfiguredDefinitions(configs map[string]agentconfig.MCPServerConfig, providers map[string]agentconfig.Config, defaults []string) *ConfiguredDefinitions {
	c := &ConfiguredDefinitions{}
	for id := range providers {
		c.providers = append(c.providers, id)
	}
	sort.Strings(c.providers)
	for id, config := range configs {
		if id == "balda" {
			continue
		}
		d := mcpcmd.Definition{Transport: mcpcmd.Transport(config.Type), URL: config.URL, Args: append([]string(nil), config.Args...), Directory: config.WorkingDir, Env: redactedConfiguredBindings(config.Env), Headers: redactedConfiguredBindings(config.Headers)}
		if len(config.Cmd) > 0 {
			d.Command = config.Cmd[0]
			d.Args = append(append([]string(nil), config.Cmd[1:]...), d.Args...)
		}
		for _, provider := range c.providers {
			for _, server := range providers[provider].MCPServers {
				if server == id {
					d.Targets.Providers = append(d.Targets.Providers, provider)
					break
				}
			}
		}
		if slices.Contains(defaults, id) {
			d.Targets = mcpcmd.Targets{All: true}
		}
		c.items = append(c.items, mcpcmd.Item{Connection: mcpcmd.Connection{ID: "config:" + id, PublicID: id, Source: mcpcmd.SourceConfig, Enabled: true}, Definition: d, Status: mcpcmd.StatusPending})
	}
	sort.Slice(c.items, func(i, j int) bool { return c.items[i].Connection.PublicID < c.items[j].Connection.PublicID })
	return c
}

func redactedConfiguredBindings(values map[string]string) map[string]mcpcmd.ValueBinding {
	if len(values) == 0 {
		return nil
	}
	bindings := make(map[string]mcpcmd.ValueBinding, len(values))
	for key := range values {
		bindings[key] = mcpcmd.ValueBinding{Kind: mcpcmd.ValueProtected}
	}
	return bindings
}

// MCPDefinitions returns independent public metadata for file-owned entries.
func (c *ConfiguredDefinitions) MCPDefinitions(context.Context) ([]mcpcmd.Item, error) {
	items := make([]mcpcmd.Item, len(c.items))
	for i, item := range c.items {
		items[i] = item
		items[i].Definition = cloneDefinition(item.Definition)
	}
	return items, nil
}

// ProviderIDs returns the configured target names in stable order.
func (c *ConfiguredDefinitions) ProviderIDs(context.Context) ([]string, error) {
	return append([]string(nil), c.providers...), nil
}

func cloneDefinition(d mcpcmd.Definition) mcpcmd.Definition {
	d.Args = append([]string(nil), d.Args...)
	d.Targets.Providers = append([]string(nil), d.Targets.Providers...)
	d.Scopes = append([]string(nil), d.Scopes...)
	d.Env = maps.Clone(d.Env)
	d.Headers = maps.Clone(d.Headers)
	return d
}
