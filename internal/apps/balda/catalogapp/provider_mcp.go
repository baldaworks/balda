package catalogapp

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
)

// AcquireProviderMCPServerIDs pins the union once while preserving each
// provider's configured defaults and exact managed revision targets.
func (r *Runtime) AcquireProviderMCPServerIDs(ctx context.Context, snapshotID runtimecatalogcmd.SnapshotID, configured map[string][]string) (map[string][]string, func(), error) {
	snapshot, err := r.retainedSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, nil, err
	}
	selected := make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor)
	result := make(map[string][]string, len(configured))
	for providerID, names := range configured {
		result[providerID] = []string{}
		for _, name := range names {
			name = strings.TrimSpace(name)
			// Bundled MCP is owned by the existing host/session binder.
			if name == "balda" {
				result[providerID] = append(result[providerID], name)
			}
		}
	}
	for _, descriptor := range snapshot.MCPServers {
		kind := descriptor.ID.Source.Kind
		if kind != runtimecatalogcmd.SourceKindManagedMCP && kind != runtimecatalogcmd.SourceKindPlugin && kind != runtimecatalogcmd.SourceKindConfiguredMCP {
			continue
		}
		all := kind == runtimecatalogcmd.SourceKindPlugin
		var targets []string
		if kind == runtimecatalogcmd.SourceKindConfiguredMCP {
			if !descriptor.TargetingKnown {
				return nil, nil, runtimecatalogcmd.ErrRevisionUnavailable
			}
			targets = descriptor.TargetProviderIDs
		} else if !all {
			revision, found, err := r.managedMCP.GetMCPRevision(ctx, descriptor.ConfigRef, string(descriptor.Revision))
			if err != nil || !found || revision.ConnectionID != descriptor.ConfigRef || revision.ID != string(descriptor.Revision) {
				return nil, nil, runtimecatalogcmd.ErrRevisionUnavailable
			}
			all, targets = revision.Definition.Targets.All, revision.Definition.Targets.Providers
		}
		for providerID := range configured {
			if all || slices.Contains(targets, providerID) {
				selected[descriptor.ID] = descriptor
				result[providerID] = append(result[providerID], descriptorRegistryID(descriptor))
			}
		}
	}
	descriptors := make([]runtimecatalogcmd.MCPServerDescriptor, 0, len(selected))
	for _, descriptor := range selected {
		descriptors = append(descriptors, descriptor)
	}
	_, release, err := r.mcp.AcquireDescriptors(ctx, descriptors)
	if err != nil {
		return nil, nil, err
	}
	for providerID, ids := range result {
		sort.Strings(ids)
		result[providerID] = slices.Compact(ids)
	}
	return result, release, nil
}

func descriptorRegistryID(descriptor runtimecatalogcmd.MCPServerDescriptor) string {
	return mcpfx.RegistryID(mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name})
}

func providerMCPDefaults(providers map[string]agentconfig.Config, root string, extra []string) (map[string][]string, error) {
	result := make(map[string][]string)
	visiting := make(map[string]bool)
	validator := agentfactory.New(providers, mcpregistry.New(nil))
	var visit func(string) error
	visit = func(id string) error {
		id = strings.TrimSpace(id)
		if visiting[id] {
			return fmt.Errorf("provider pool contains a cycle")
		}
		if _, ok := result[id]; ok {
			return nil
		}
		config, ok := providers[id]
		if !ok {
			return fmt.Errorf("provider is unavailable")
		}
		// Reuse the provider owner's normalization/schema check in this
		// existing selection pass, before MCP readiness can stop construction.
		if err := validator.ValidateAgent(id); err != nil {
			return fmt.Errorf("selected provider configuration is invalid")
		}
		visiting[id] = true
		result[id] = append([]string{}, config.MCPServers...)
		if agentconfig.IsPoolType(config.Type) {
			if config.PoolConfig == nil || len(config.PoolConfig.Members) == 0 {
				return fmt.Errorf("provider pool has no members")
			}
			for _, member := range config.PoolConfig.Members {
				if next, ok := providers[strings.TrimSpace(member)]; ok && agentconfig.IsPoolType(next.Type) {
					return fmt.Errorf("provider pool contains a nested pool")
				}
				if err := visit(member); err != nil {
					return err
				}
			}
		}
		delete(visiting, id)
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	result[root] = append(result[root], extra...)
	return result, nil
}

// configureProviderMCP captures current file selection once, before catalog
// publication. Provider acquisition later uses the retained targeting metadata.
func (r *Runtime) configureProviderMCP(providers map[string]agentconfig.Config, root string, extra []string) error {
	selected := make(map[string][]string)
	for id, provider := range providers {
		names := append([]string(nil), provider.MCPServers...)
		if id == root {
			names = append(names, extra...)
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" || name == "balda" {
				continue
			}
			selected[name] = append(selected[name], id)
		}
	}
	for i := range r.configuredMCP {
		name := r.configuredMCP[i].Descriptor.ID.Name
		targets := selected[name]
		sort.Strings(targets)
		r.configuredMCP[i].MCPServers[0].TargetProviderIDs = slices.Compact(targets)
		r.configuredMCP[i].MCPServers[0].TargetingKnown = true
		delete(selected, name)
	}
	if len(selected) != 0 {
		return runtimecatalogcmd.ErrRevisionUnavailable
	}
	return nil
}
