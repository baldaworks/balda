package agentplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	pluginManifestSchemaV1 = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	pluginsDirName         = "plugins"
	skillsDirName          = "skills"
	skillFileName          = "SKILL.md"
)

type Catalog struct {
	plugins []Plugin
	skills  []Skill
}

type Plugin struct {
	Name        string
	Version     string
	Description string
	RootDir     string
}

type Skill struct {
	PluginName string
	Name       string
	Dir        string
	Path       string
	Content    string
}

type Loader struct {
	rootDir string
}

func NewLoader(stateDir string) (*Loader, error) {
	trimmed := strings.TrimSpace(stateDir)
	if trimmed == "" {
		return nil, fmt.Errorf("state dir is required")
	}
	return &Loader{rootDir: filepath.Join(trimmed, pluginsDirName)}, nil
}

func (l *Loader) Load() (*Catalog, error) {
	if l == nil {
		return nil, fmt.Errorf("loader is required")
	}
	entries, err := os.ReadDir(l.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &Catalog{}, nil
		}
		return nil, fmt.Errorf("read plugins dir: %w", err)
	}

	catalog := &Catalog{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(l.rootDir, entry.Name())
		plugin, skills, err := loadPlugin(root)
		if err != nil {
			continue
		}
		catalog.plugins = append(catalog.plugins, plugin)
		catalog.skills = append(catalog.skills, skills...)
	}

	sort.Slice(catalog.plugins, func(i, j int) bool {
		return catalog.plugins[i].Name < catalog.plugins[j].Name
	})
	sort.Slice(catalog.skills, func(i, j int) bool {
		if catalog.skills[i].PluginName == catalog.skills[j].PluginName {
			return catalog.skills[i].Name < catalog.skills[j].Name
		}
		return catalog.skills[i].PluginName < catalog.skills[j].PluginName
	})
	return catalog, nil
}

func (c *Catalog) Skills() []Skill {
	if c == nil || len(c.skills) == 0 {
		return nil
	}
	out := make([]Skill, len(c.skills))
	copy(out, c.skills)
	return out
}

func (c *Catalog) Plugins() []Plugin {
	if c == nil || len(c.plugins) == 0 {
		return nil
	}
	out := make([]Plugin, len(c.plugins))
	copy(out, c.plugins)
	return out
}

type manifest struct {
	Schema      string `json:"$schema"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

func loadPlugin(root string) (Plugin, []Skill, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Plugin{}, nil, fmt.Errorf("resolve plugin root: %w", err)
	}
	manifestPath := filepath.Join(resolvedRoot, "plugin.json")
	if !pathWithinRoot(resolvedRoot, manifestPath) {
		return Plugin{}, nil, fmt.Errorf("manifest escapes root")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Plugin{}, nil, fmt.Errorf("read manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Plugin{}, nil, fmt.Errorf("decode manifest: %w", err)
	}
	if strings.TrimSpace(m.Schema) != pluginManifestSchemaV1 {
		return Plugin{}, nil, fmt.Errorf("unsupported plugin schema")
	}
	if strings.TrimSpace(m.Name) == "" {
		return Plugin{}, nil, fmt.Errorf("plugin name is required")
	}

	plugin := Plugin{
		Name:        strings.TrimSpace(m.Name),
		Version:     strings.TrimSpace(m.Version),
		Description: strings.TrimSpace(m.Description),
		RootDir:     resolvedRoot,
	}
	skills, err := loadSkills(resolvedRoot, plugin.Name)
	if err != nil {
		return Plugin{}, nil, err
	}
	return plugin, skills, nil
}

func loadSkills(root, pluginName string) ([]Skill, error) {
	skillsRoot := filepath.Join(root, skillsDirName)
	info, err := os.Stat(skillsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat skills dir: %w", err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	if !pathWithinRoot(root, skillsRoot) {
		return nil, fmt.Errorf("skills dir escapes root")
	}
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(skillsRoot, entry.Name())
		skillPath := filepath.Join(skillDir, skillFileName)
		if !pathWithinRoot(root, skillPath) {
			continue
		}
		info, err := os.Stat(skillPath)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		skills = append(skills, Skill{
			PluginName: pluginName,
			Name:       entry.Name(),
			Dir:        skillDir,
			Path:       skillPath,
			Content:    content,
		})
	}
	return skills, nil
}

func pathWithinRoot(root, target string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolvedTarget = filepath.Clean(target)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
