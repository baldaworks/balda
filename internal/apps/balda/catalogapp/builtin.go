package catalogapp

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const (
	builtinSourceName    = "balda"
	builtinSystemSkill   = "balda-system"
	builtinSkillResource = "balda-system/SKILL.md"
)

var (
	//go:embed builtin
	embeddedBuiltinFiles embed.FS

	builtinSourceID = runtimecatalogcmd.SourceID{
		Kind: runtimecatalogcmd.SourceKindBuiltin,
		Name: builtinSourceName,
	}
)

func buildBuiltinSource(loader *runtimecatalog.SourceLoader, advertisements []commandcmd.Advertisement) (runtimecatalogcmd.Source, fs.FS, error) {
	if loader == nil {
		return runtimecatalogcmd.Source{}, nil, errors.New("built-in source loader is required")
	}
	root, err := fs.Sub(embeddedBuiltinFiles, "builtin")
	if err != nil {
		return runtimecatalogcmd.Source{}, nil, fmt.Errorf("open embedded built-in source: %w", err)
	}
	source, err := loader.LoadBuiltinSkillSource(root, builtinSourceID)
	if err != nil {
		return runtimecatalogcmd.Source{}, nil, fmt.Errorf("load embedded built-in source: %w", err)
	}
	if err := validateBuiltinSource(source); err != nil {
		return runtimecatalogcmd.Source{}, nil, err
	}

	names := make(map[string]struct{})
	for _, advertisement := range advertisements {
		for _, name := range advertisement.Names {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		source.Commands = append(source.Commands, runtimecatalogcmd.CommandDescriptor{
			ID: runtimecatalogcmd.ContributionID{
				Source: builtinSourceID,
				Kind:   runtimecatalogcmd.ContributionKindCommand,
				Name:   name,
			},
			Revision:   source.Descriptor.Revision,
			Name:       name,
			Advertised: true,
		})
	}
	return source, root, nil
}

func validateBuiltinSource(source runtimecatalogcmd.Source) error {
	if source.Descriptor.ID != builtinSourceID || source.Descriptor.Revision == "" {
		return errors.New("invalid built-in source descriptor")
	}
	if len(source.Diagnostics) != 0 || len(source.Skills) != 1 {
		return errors.New("built-in source must contain exactly one valid skill")
	}
	skill := source.Skills[0]
	wantID := runtimecatalogcmd.ContributionID{
		Source: builtinSourceID,
		Kind:   runtimecatalogcmd.ContributionKindSkill,
		Name:   builtinSystemSkill,
	}
	if skill.ID != wantID || skill.Name != builtinSystemSkill || skill.Resource != builtinSkillResource || skill.Revision != source.Descriptor.Revision {
		return errors.New("invalid built-in system skill identity")
	}
	return nil
}

func validateBuiltinSnapshot(snapshot runtimecatalogcmd.Snapshot, builtin runtimecatalogcmd.Source) error {
	descriptor, ok := snapshot.Sources[builtinSourceID]
	if !ok || descriptor != builtin.Descriptor {
		return runtimecatalogcmd.ErrRevisionUnavailable
	}
	wantCommands := make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor, len(builtin.Commands))
	for _, command := range builtin.Commands {
		wantCommands[command.ID] = command
		candidate, ok := snapshot.Commands[command.ID]
		if !ok || candidate != command {
			return runtimecatalogcmd.ErrRevisionUnavailable
		}
	}
	for id, candidate := range snapshot.Commands {
		if id.Source != builtinSourceID {
			continue
		}
		if expected, ok := wantCommands[id]; !ok || candidate != expected {
			return runtimecatalogcmd.ErrRevisionUnavailable
		}
	}
	wantSkills := make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata, len(builtin.Skills))
	for _, skill := range builtin.Skills {
		wantSkills[skill.ID] = skill
		candidate, ok := snapshot.Skills[skill.ID]
		if !ok || candidate != skill {
			return runtimecatalogcmd.ErrRevisionUnavailable
		}
	}
	for id, candidate := range snapshot.Skills {
		if id.Source != builtinSourceID {
			continue
		}
		if expected, ok := wantSkills[id]; !ok || candidate != expected {
			return runtimecatalogcmd.ErrRevisionUnavailable
		}
	}
	for id := range snapshot.MCPServers {
		if id.Source == builtinSourceID {
			return runtimecatalogcmd.ErrRevisionUnavailable
		}
	}
	return nil
}
