package mcpfx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

// ConfiguredDefinitions separates redacted inventory metadata from private
// file values used only to capture protected OAuth revisions.
type ConfiguredDefinitions struct {
	items     []mcpcmd.Item
	providers []string
	values    map[string]mcpcmd.LaunchValues
}

// NewConfiguredDefinitions translates current Norma configuration for the UI.
func NewConfiguredDefinitions(configs map[string]agentconfig.MCPServerConfig, providers map[string]agentconfig.Config, root string, defaults []string) *ConfiguredDefinitions {
	c := &ConfiguredDefinitions{values: make(map[string]mcpcmd.LaunchValues)}
	for id := range providers {
		c.providers = append(c.providers, id)
	}
	sort.Strings(c.providers)
	for id, config := range configs {
		if id == "balda" {
			continue
		}
		c.values[id] = mcpcmd.LaunchValues{Env: maps.Clone(config.Env), Headers: maps.Clone(config.Headers)}
		d := mcpcmd.Definition{ConfigRevision: ConfiguredRevision(config), Transport: mcpcmd.Transport(config.Type), URL: config.URL, Args: append([]string(nil), config.Args...), Directory: config.WorkingDir, Env: redactedConfiguredBindings(config.Env), Headers: redactedConfiguredBindings(config.Headers)}
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
		if slices.Contains(defaults, id) && !slices.Contains(d.Targets.Providers, root) {
			d.Targets.Providers = append(d.Targets.Providers, root)
			sort.Strings(d.Targets.Providers)
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
	if d.AuthBinding != nil {
		binding := *d.AuthBinding
		d.AuthBinding = &binding
	}
	d.Env = maps.Clone(d.Env)
	d.Headers = maps.Clone(d.Headers)
	return d
}

// MCPAuthorizationDefinition returns private values exclusively to the trusted
// capture port. Ordinary inventory remains redacted and independently cloned.
func (c *ConfiguredDefinitions) MCPAuthorizationDefinition(ctx context.Context, id string) (mcpcmd.Definition, mcpcmd.LaunchValues, error) {
	if err := ctx.Err(); err != nil {
		return mcpcmd.Definition{}, mcpcmd.LaunchValues{}, err
	}
	for _, item := range c.items {
		if item.Connection.PublicID == id {
			values := c.values[id]
			return cloneDefinition(item.Definition), mcpcmd.LaunchValues{Env: maps.Clone(values.Env), Headers: maps.Clone(values.Headers)}, nil
		}
	}
	return mcpcmd.Definition{}, mcpcmd.LaunchValues{}, mcpcmd.ErrNotFound
}

// ConfiguredRevision includes private values in the exact file identity; only
// the opaque digest enters public metadata.
func ConfiguredRevision(config agentconfig.MCPServerConfig) string {
	data, _ := json.Marshal(config)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
