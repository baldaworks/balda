# Plugin Marketplace Repo Format

Owner: Balda maintainers  
Status: draft

## Goal

Define a temporary repository index format for Balda plugin marketplaces while
the Agent Plugins ecosystem does not yet publish a standardized marketplace or
distribution format.

This format is intentionally narrow:

- plugin package layout follows the Agent Plugins portable package model;
- Balda adds only a repository-level index at `.agents/plugins/marketplace.json`;
- Balda-specific extension metadata is out of scope for v0.

## Repository layout

```text
repo/
  .agents/
    plugins/
      marketplace.json

  plugins/
    <plugin-name>/
      plugin.json
      skills/
      mcp.json
```

## Invariants

- `.agents/plugins/marketplace.json` is the root marketplace index for the
  repository.
- `plugins/<plugin-name>/` is a standard plugin package root.
- `plugin.json` is required for each indexed plugin package.
- `skills/` and `mcp.json` are optional and follow the Agent Plugins portable
  package format.
- The marketplace index is a discovery/install catalog only. It does not
  duplicate the full plugin manifest.
- Balda-specific extension metadata is not part of v0.

## Marketplace index

Minimal shape:

```json
{
  "name": "metalagman",
  "plugins": [
    {
      "name": "hostplugin",
      "path": "./plugins/hostplugin"
    }
  ]
}
```

### Required fields

Marketplace object:

- `name`
- `plugins`

Plugin entry object:

- `name`
- `path`
- `manifest_path` optional, relative path to the plugin manifest inside the
  plugin package root; defaults to `plugin.json`

## Validation rules

- `plugins[].path` must be a relative path from repository root.
- `plugins[].path` must resolve to a plugin package directory.
- `plugins[].manifest_path` if present must be a relative path inside the
  package root.
- `plugins[].path` + `plugins[].manifest_path` (or default lookup) must resolve
  to the plugin manifest.
- `plugins[].name` must match `plugin.json.name`.
- Plugin names must be unique within one marketplace index.

## Semantics

- `marketplace.json` is the repository index only.
- the manifest referenced by `manifest_path` is the source of truth when
  present.
- without `manifest_path`, Balda resolves the portable manifest from
  `plugin.json`, then `.plugin/plugin.json`.
- host-specific manifest locations such as `.codex-plugin/` are not part of
  marketplace discovery.
- Balda runtime installation may materialize indexed packages into its local
  plugin state, where the current loader reads standard plugin packages from
  `stateDir/plugins/<name>/plugin.json`.
- If Agent Plugins later defines a standard marketplace or distribution format,
  Balda should replace only the repository index layer and keep plugin package
  layout unchanged.

## Minimal valid example

```text
repo/
  .agents/plugins/marketplace.json
  plugins/hostplugin/plugin.json
```

`/.agents/plugins/marketplace.json`

```json
{
  "name": "metalagman",
  "plugins": [
    {
      "name": "hostplugin",
      "path": "./plugins/hostplugin"
    }
  ]
}
```

`/plugins/hostplugin/plugin.json`

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "hostplugin",
  "display_name": "HostPlugin",
  "description": "Portable plugin package."
}
```

## Example with hidden manifest directory

If a repository keeps the portable plugin manifest under a nested directory,
the marketplace entry must point to it explicitly:

```json
{
  "name": "prism",
  "plugins": [
    {
      "name": "prism",
      "source": { "source": "local", "path": "./plugins/prism" },
      "manifest_path": ".plugin/plugin.json"
    }
  ]
}
```
