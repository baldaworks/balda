package catalogapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

const configuredUpgradeKeyPrefix = "runtime_catalog_configured_upgrade:"

// This is the exact descriptor representation selected by the SQL migration.
// Keeping its decoder separate rejects targeting/capture fields and unknown
// fields rather than treating an incomplete current descriptor as an old pin.
type configuredUpgradePin struct {
	ID        runtimecatalogcmd.ContributionID `json:"id"`
	Revision  runtimecatalogcmd.RevisionID     `json:"revision"`
	Name      string                           `json:"name"`
	Transport string                           `json:"transport"`
}

type configuredUpgradeMarker struct {
	Version    int                  `json:"version"`
	Descriptor configuredUpgradePin `json:"descriptor"`
}

// configuredUpgradeDescriptor consumes only SQL-migrated historical pins. It
// never writes migration state, rewrites a snapshot, or accepts an arbitrary
// revision alias. The old producer covered structure and binding keys only;
// values still come from the host file under that historical contract.
func (r *Runtime) configuredUpgradeDescriptor(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (runtimecatalogcmd.MCPServerDescriptor, error) {
	if descriptor.TargetingKnown || len(descriptor.TargetProviderIDs) != 0 || descriptor.ConfigRef != "" || descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindConfiguredMCP || descriptor.ID.Kind != runtimecatalogcmd.ContributionKindMCPServer || descriptor.ID.Source.Name != descriptor.Name || descriptor.ID.Name != descriptor.Name || descriptor.Name == "" || len(descriptor.Revision) != 64 {
		return runtimecatalogcmd.MCPServerDescriptor{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	// Revision digests have a fixed length, so this key remains unambiguous
	// even when a configured name contains a colon.
	key := configuredUpgradeKeyPrefix + descriptor.Name + ":" + string(descriptor.Revision)
	raw, found, err := r.kv.GetJSON(ctx, key)
	if err != nil {
		return runtimecatalogcmd.MCPServerDescriptor{}, fmt.Errorf("read configured MCP upgrade marker: %w", err)
	}
	if !found {
		return runtimecatalogcmd.MCPServerDescriptor{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return runtimecatalogcmd.MCPServerDescriptor{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	var marker configuredUpgradeMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil || marker.Version != 1 || marker.Descriptor != (configuredUpgradePin{ID: descriptor.ID, Revision: descriptor.Revision, Name: descriptor.Name, Transport: descriptor.Transport}) || r.configuredUpgradeRevisions[descriptor.ID] != descriptor.Revision {
		return runtimecatalogcmd.MCPServerDescriptor{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	for _, source := range r.configuredMCP {
		current := source.MCPServers[0]
		if current.ID == descriptor.ID && current.Name == descriptor.Name && current.Transport == descriptor.Transport {
			return current, nil
		}
	}
	return runtimecatalogcmd.MCPServerDescriptor{}, runtimecatalogcmd.ErrRevisionUnavailable
}

// configuredUpgradeResolver translates only a validated SQL marker's launch
// request. The outer bridge and reconciler keep the original pinned identity.
type configuredUpgradeResolver struct {
	catalog *Runtime
	current mcpruntime.LaunchResolver
}

func (r configuredUpgradeResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if !descriptor.TargetingKnown {
		translated, err := r.catalog.configuredUpgradeDescriptor(ctx, descriptor)
		if err != nil {
			return mcpruntime.LaunchConfig{}, err
		}
		descriptor = translated
	}
	return r.current.ResolveLaunch(ctx, descriptor)
}

func configuredUpgradeRevisions(configs map[string]agentconfig.MCPServerConfig) map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.RevisionID {
	revisions := make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.RevisionID, len(configs))
	for name, config := range configs {
		envKeys := configuredBindingKeys(config.Env)
		headerKeys := configuredBindingKeys(config.Headers)
		// Field names/order are the persisted pre-upgrade producer's digest
		// contract. Do not use this projection for new configured revisions.
		projection := struct {
			Type       agentconfig.MCPServerType `json:"type"`
			Cmd        []string                  `json:"cmd,omitempty"`
			Args       []string                  `json:"args,omitempty"`
			WorkingDir string                    `json:"working_dir,omitempty"`
			URL        string                    `json:"url,omitempty"`
			EnvKeys    []string                  `json:"env_keys,omitempty"`
			HeaderKeys []string                  `json:"header_keys,omitempty"`
		}{config.Type, config.Cmd, config.Args, config.WorkingDir, config.URL, envKeys, headerKeys}
		data, _ := json.Marshal(projection)
		id := runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: name}, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: name}
		revisions[id] = hashValue(string(data))
	}
	return revisions
}

func configuredBindingKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
