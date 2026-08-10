package pluginapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baldagit "github.com/normahq/balda/internal/git"
	"github.com/normahq/balda/internal/apps/balda/agentplugin"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type PluginSummary struct {
	Name        string
	Version     string
	Description string
}

type AvailablePlugin struct {
	Name             string
	DisplayName      string
	Description      string
	Version          string
	Marketplace      string
	MarketplaceLabel string
	Category         string
	SourceRoot       string
	PluginPath       string
	Installed        bool
}

type Service struct {
	stateDir string
	kv       baldastate.KVStore
}

type MarketplaceSource struct {
	Name   string
	Source string
	Ref    string
	Sparse []string
}

type MarketplaceStatus struct {
	Name              string
	Source            string
	Kind              string
	Ref               string
	Sparse            []string
	CachePath         string
	Cached            bool
	LastRefreshedAt   string
	ResolvedRef       string
	ManifestPath      string
	ManifestPresent   bool
	AvailablePlugins  int
}

type MarketplaceUpgradeResult struct {
	Name         string
	Source       string
	PluginCount  int
	Refreshed    bool
	ManifestPath string
	Status       MarketplaceStatus
}

type marketplaceSourceKind string

const (
	marketplaceSourceKindInvalid marketplaceSourceKind = ""
	marketplaceSourceKindLocal   marketplaceSourceKind = "local"
	marketplaceSourceKindGit     marketplaceSourceKind = "git"
)

type marketplaceSourceSpec struct {
	Kind      marketplaceSourceKind
	Name      string
	RawSource string
	LocalRoot string
	GitURL    string
	Ref       string
	Sparse    []string
	CacheRoot string
	RepoRoot  string
}

func New(stateDir string, kv baldastate.KVStore) (*Service, error) {
	trimmed := strings.TrimSpace(stateDir)
	if trimmed == "" {
		return nil, fmt.Errorf("state dir is required")
	}
	return &Service{stateDir: trimmed, kv: kv}, nil
}

func InferMarketplaceName(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	trimmed = strings.TrimRight(trimmed, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	if idx := strings.LastIndex(trimmed, ":"); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	return trimmed
}

func (s *Service) ListInstalled(ctx context.Context) ([]PluginSummary, error) {
	_ = ctx
	catalog, err := s.loadCatalog()
	if err != nil {
		return nil, err
	}
	plugins := catalog.Plugins()
	if len(plugins) == 0 {
		return nil, nil
	}
	out := make([]PluginSummary, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, PluginSummary{
			Name:        plugin.Name,
			Version:     plugin.Version,
			Description: plugin.Description,
		})
	}
	return out, nil
}

func (s *Service) GetInstalled(ctx context.Context, name string) (PluginSummary, bool, error) {
	_ = ctx
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return PluginSummary{}, false, nil
	}
	catalog, err := s.loadCatalog()
	if err != nil {
		return PluginSummary{}, false, err
	}
	for _, plugin := range catalog.Plugins() {
		if plugin.Name == trimmed {
			return PluginSummary{
				Name:        plugin.Name,
				Version:     plugin.Version,
				Description: plugin.Description,
			}, true, nil
		}
	}
	return PluginSummary{}, false, nil
}

func (s *Service) loadCatalog() (*agentplugin.Catalog, error) {
	loader, err := agentplugin.NewLoader(s.stateDir)
	if err != nil {
		return nil, err
	}
	return loader.Load()
}

const marketplacePrefix = "plugin_marketplace:"
const marketplaceManifestPath = ".agents/plugins/marketplace.json"

type marketplaceIndex struct {
	Name      string `json:"name"`
	Interface struct {
		DisplayName string `json:"displayName"`
	} `json:"interface"`
	Plugins []marketplacePlugin `json:"plugins"`
}

type marketplacePlugin struct {
	Name   string `json:"name"`
	Source struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	} `json:"source"`
	ManifestPath string `json:"manifest_path"`
	Category     string `json:"category"`
}

func (s *Service) ListMarketplaces(ctx context.Context) ([]MarketplaceSource, error) {
	if s.kv == nil {
		return nil, nil
	}
	keys, err := s.kv.List(ctx, marketplacePrefix)
	if err != nil {
		return nil, err
	}
	out := make([]MarketplaceSource, 0, len(keys))
	for _, key := range keys {
		raw, ok, err := s.kv.GetJSON(ctx, key)
		if err != nil {
			return nil, err
		}
		if !ok || raw == nil {
			continue
		}
		var src MarketplaceSource
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &src); err != nil {
			return nil, err
		}
		if strings.TrimSpace(src.Name) == "" || strings.TrimSpace(src.Source) == "" {
			continue
		}
		out = append(out, normalizeSource(src))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) ListMarketplaceStatuses(ctx context.Context) ([]MarketplaceStatus, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MarketplaceStatus, 0, len(sources))
	for _, src := range sources {
		status, err := s.marketplaceStatus(ctx, src)
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

func (s *Service) GetMarketplaceStatus(ctx context.Context, name string) (MarketplaceStatus, bool, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return MarketplaceStatus{}, false, nil
	}
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return MarketplaceStatus{}, false, err
	}
	for _, src := range sources {
		if src.Name != trimmed {
			continue
		}
		status, err := s.marketplaceStatus(ctx, src)
		if err != nil {
			return MarketplaceStatus{}, false, err
		}
		return status, true, nil
	}
	return MarketplaceStatus{}, false, nil
}

func (s *Service) AddMarketplace(ctx context.Context, src MarketplaceSource) error {
	if s.kv == nil {
		return fmt.Errorf("marketplace store is unavailable")
	}
	normalized := normalizeSource(src)
	if normalized.Name == "" {
		return fmt.Errorf("marketplace name is required")
	}
	if normalized.Source == "" {
		return fmt.Errorf("marketplace source is required")
	}
	return s.kv.SetJSON(ctx, marketplacePrefix+normalized.Name, normalized)
}

func (s *Service) RemoveMarketplace(ctx context.Context, name string) error {
	if s.kv == nil {
		return fmt.Errorf("marketplace store is unavailable")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("marketplace name is required")
	}
	return s.kv.Delete(ctx, marketplacePrefix+trimmed)
}

func (s *Service) UpgradeMarketplaces(ctx context.Context, name string) ([]MarketplaceUpgradeResult, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		filtered := make([]MarketplaceSource, 0, 1)
		for _, src := range sources {
			if src.Name == trimmedName {
				filtered = append(filtered, src)
				break
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("marketplace not found")
		}
		sources = filtered
	}

	results := make([]MarketplaceUpgradeResult, 0, len(sources))
	for _, src := range sources {
		root, err := s.materializeMarketplaceSource(ctx, src)
		if err != nil {
			return nil, err
		}
		manifestPath := filepath.Join(root, marketplaceManifestPath)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("read marketplace manifest for %q: %w", src.Name, err)
		}
		var index marketplaceIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, fmt.Errorf("decode marketplace manifest for %q: %w", src.Name, err)
		}
		if strings.TrimSpace(index.Name) == "" {
			index.Name = src.Name
		}
		if strings.TrimSpace(index.Name) == "" {
			return nil, fmt.Errorf("marketplace name is required")
		}
		plugins, err := s.readMarketplacePlugins(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("load marketplace plugins for %q: %w", src.Name, err)
		}
		if normalized := normalizeSource(src); s.kv != nil {
			if err := s.kv.SetJSON(ctx, marketplacePrefix+normalized.Name, normalized); err != nil {
				return nil, err
			}
		}
		results = append(results, MarketplaceUpgradeResult{
			Name:         src.Name,
			Source:       src.Source,
			PluginCount:  len(plugins),
			Refreshed:    true,
			ManifestPath: manifestPath,
			Status:       s.mustMarketplaceStatus(ctx, src, len(plugins), manifestPath),
		})
	}
	return results, nil
}

func (s *Service) mustMarketplaceStatus(ctx context.Context, src MarketplaceSource, pluginCount int, manifestPath string) MarketplaceStatus {
	status, err := s.marketplaceStatus(ctx, src)
	if err != nil {
		return MarketplaceStatus{
			Name:             src.Name,
			Source:           src.Source,
			Ref:              src.Ref,
			Sparse:           append([]string(nil), src.Sparse...),
			ManifestPath:     manifestPath,
			ManifestPresent:  true,
			AvailablePlugins: pluginCount,
		}
	}
	if pluginCount >= 0 {
		status.AvailablePlugins = pluginCount
	}
	if strings.TrimSpace(manifestPath) != "" {
		status.ManifestPath = manifestPath
		status.ManifestPresent = true
	}
	return status
}

func normalizeSource(src MarketplaceSource) MarketplaceSource {
	src.Name = strings.TrimSpace(src.Name)
	src.Source = strings.TrimSpace(src.Source)
	src.Ref = strings.TrimSpace(src.Ref)
	if len(src.Sparse) > 0 {
		out := src.Sparse[:0]
		for _, path := range src.Sparse {
			trimmed := strings.TrimSpace(path)
			if trimmed == "" {
				continue
			}
			out = append(out, trimmed)
		}
		src.Sparse = out
	}
	return src
}

func (s *Service) ListAvailable(ctx context.Context) ([]AvailablePlugin, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := s.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	installedSet := make(map[string]PluginSummary, len(installed))
	for _, plugin := range installed {
		installedSet[plugin.Name] = plugin
	}
	out := make([]AvailablePlugin, 0)
	for _, src := range sources {
		plugins, err := s.readMarketplacePlugins(ctx, src)
		if err != nil {
			continue
		}
		for _, plugin := range plugins {
			if current, ok := installedSet[plugin.Name]; ok {
				plugin.Installed = true
				if strings.TrimSpace(plugin.Description) == "" {
					plugin.Description = current.Description
				}
				if strings.TrimSpace(plugin.Version) == "" {
					plugin.Version = current.Version
				}
			}
			out = append(out, plugin)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Marketplace == out[j].Marketplace {
			return out[i].Name < out[j].Name
		}
		return out[i].Marketplace < out[j].Marketplace
	})
	return out, nil
}

func (s *Service) GetAvailable(ctx context.Context, selector string) (AvailablePlugin, bool, error) {
	name, marketplace := splitSelector(selector)
	plugins, err := s.ListAvailable(ctx)
	if err != nil {
		return AvailablePlugin{}, false, err
	}
	var match AvailablePlugin
	found := false
	for _, plugin := range plugins {
		if plugin.Name != name {
			continue
		}
		if marketplace != "" && plugin.Marketplace != marketplace {
			continue
		}
		if found && marketplace == "" {
			return AvailablePlugin{}, false, fmt.Errorf("multiple marketplace matches for plugin %q", name)
		}
		match = plugin
		found = true
	}
	return match, found, nil
}

func (s *Service) Install(ctx context.Context, selector string) error {
	plugin, ok, err := s.GetAvailable(ctx, selector)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin not found")
	}
	if installed, installedOK, err := s.GetInstalled(ctx, plugin.Name); err != nil {
		return err
	} else if installedOK && sameInstalledPluginVersion(installed, plugin) {
		return nil
	}
	destRoot := filepath.Join(s.stateDir, "plugins", plugin.Name)
	parentDir := filepath.Dir(destRoot)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("create plugin parent dir: %w", err)
	}
	stageRoot := destRoot + ".tmp-install"
	backupRoot := destRoot + ".bak-install"
	_ = os.RemoveAll(stageRoot)
	_ = os.RemoveAll(backupRoot)
	if err := copyDir(plugin.PluginPath, stageRoot); err != nil {
		return fmt.Errorf("copy plugin package: %w", err)
	}
	if _, err := os.Stat(destRoot); err == nil {
		if err := os.Rename(destRoot, backupRoot); err != nil {
			_ = os.RemoveAll(stageRoot)
			return fmt.Errorf("backup existing plugin root: %w", err)
		}
	}
	if err := os.Rename(stageRoot, destRoot); err != nil {
		if _, backupErr := os.Stat(backupRoot); backupErr == nil {
			_ = os.Rename(backupRoot, destRoot)
		}
		_ = os.RemoveAll(stageRoot)
		return fmt.Errorf("activate installed plugin root: %w", err)
	}
	loader, err := agentplugin.NewLoader(s.stateDir)
	if err != nil {
		rollbackInstalledPlugin(destRoot, backupRoot)
		return err
	}
	catalog, err := loader.Load()
	if err != nil {
		rollbackInstalledPlugin(destRoot, backupRoot)
		return err
	}
	for _, installed := range catalog.Plugins() {
		if installed.Name == plugin.Name {
			_ = os.RemoveAll(backupRoot)
			return nil
		}
	}
	rollbackInstalledPlugin(destRoot, backupRoot)
	return fmt.Errorf("installed plugin %q did not validate", plugin.Name)
}

func sameInstalledPluginVersion(installed PluginSummary, available AvailablePlugin) bool {
	if strings.TrimSpace(installed.Name) != strings.TrimSpace(available.Name) {
		return false
	}
	installedVersion := strings.TrimSpace(installed.Version)
	availableVersion := strings.TrimSpace(available.Version)
	return installedVersion != "" && installedVersion == availableVersion
}

func (s *Service) RemoveInstalled(ctx context.Context, name string) error {
	_, ok, err := s.GetInstalled(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin not installed")
	}
	return os.RemoveAll(filepath.Join(s.stateDir, "plugins", strings.TrimSpace(name)))
}

func splitSelector(selector string) (name string, marketplace string) {
	parts := strings.SplitN(strings.TrimSpace(selector), "@", 2)
	name = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		marketplace = strings.TrimSpace(parts[1])
	}
	return name, marketplace
}

func (s *Service) readMarketplacePlugins(ctx context.Context, src MarketplaceSource) ([]AvailablePlugin, error) {
	root, err := s.materializeMarketplaceSource(ctx, src)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, marketplaceManifestPath))
	if err != nil {
		return nil, err
	}
	var index marketplaceIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	marketplaceName := strings.TrimSpace(index.Name)
	if marketplaceName == "" {
		marketplaceName = strings.TrimSpace(src.Name)
	}
	marketplaceLabel := strings.TrimSpace(index.Interface.DisplayName)
	if marketplaceLabel == "" {
		marketplaceLabel = marketplaceName
	}
	out := make([]AvailablePlugin, 0, len(index.Plugins))
	for _, entry := range index.Plugins {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		if strings.TrimSpace(entry.Source.Source) != "local" {
			continue
		}
		pluginRoot := filepath.Join(root, filepath.Clean(strings.TrimSpace(entry.Source.Path)))
		summary, err := readPluginSummary(pluginRoot, strings.TrimSpace(entry.ManifestPath))
		if err != nil {
			continue
		}
		out = append(out, AvailablePlugin{
			Name:             summary.Name,
			DisplayName:      summary.Name,
			Description:      summary.Description,
			Version:          summary.Version,
			Marketplace:      marketplaceName,
			MarketplaceLabel: marketplaceLabel,
			Category:         strings.TrimSpace(entry.Category),
			SourceRoot:       root,
			PluginPath:       pluginRoot,
		})
	}
	return out, nil
}

func (s *Service) materializeMarketplaceSource(ctx context.Context, src MarketplaceSource) (string, error) {
	spec, err := s.marketplaceSourceSpec(src)
	if err != nil {
		return "", err
	}
	switch spec.Kind {
	case marketplaceSourceKindLocal:
		return spec.LocalRoot, nil
	case marketplaceSourceKindGit:
		if err := os.MkdirAll(spec.CacheRoot, 0o755); err != nil {
			return "", fmt.Errorf("create marketplace cache root: %w", err)
		}
		if _, err := os.Stat(filepath.Join(spec.RepoRoot, ".git")); err == nil {
			if err := updateGitMarketplaceCheckout(ctx, spec); err != nil {
				return "", err
			}
			return spec.RepoRoot, nil
		}
		if err := os.RemoveAll(spec.RepoRoot); err != nil {
			return "", fmt.Errorf("reset marketplace repo root: %w", err)
		}
		if err := cloneGitMarketplaceSource(ctx, spec); err != nil {
			return "", err
		}
		return spec.RepoRoot, nil
	default:
		return "", fmt.Errorf("unsupported marketplace source %q", strings.TrimSpace(src.Source))
	}
}

func (s *Service) marketplaceSourceSpec(src MarketplaceSource) (marketplaceSourceSpec, error) {
	normalized := normalizeSource(src)
	spec := marketplaceSourceSpec{
		Name:      normalized.Name,
		RawSource: normalized.Source,
		Ref:       normalized.Ref,
		Sparse:    append([]string(nil), normalized.Sparse...),
	}
	if spec.Name == "" {
		spec.Name = InferMarketplaceName(spec.RawSource)
	}
	if root := resolveLocalMarketplaceRoot(spec.RawSource); root != "" {
		spec.Kind = marketplaceSourceKindLocal
		spec.LocalRoot = root
		return spec, nil
	}
	if isGitMarketplaceSource(spec.RawSource) {
		spec.Kind = marketplaceSourceKindGit
		spec.GitURL = spec.RawSource
		spec.CacheRoot = filepath.Join(s.stateDir, "plugin-marketplaces", spec.Name)
		spec.RepoRoot = filepath.Join(spec.CacheRoot, "repo")
		return spec, nil
	}
	return marketplaceSourceSpec{}, fmt.Errorf("unsupported marketplace source %q", spec.RawSource)
}

func (s *Service) marketplaceStatus(ctx context.Context, src MarketplaceSource) (MarketplaceStatus, error) {
	spec, err := s.marketplaceSourceSpec(src)
	if err != nil {
		return MarketplaceStatus{}, err
	}
	status := MarketplaceStatus{
		Name:   spec.Name,
		Source: spec.RawSource,
		Kind:   string(spec.Kind),
		Ref:    spec.Ref,
		Sparse: append([]string(nil), spec.Sparse...),
	}
	switch spec.Kind {
	case marketplaceSourceKindLocal:
		status.CachePath = spec.LocalRoot
		status.Cached = true
		status.ManifestPath = filepath.Join(spec.LocalRoot, marketplaceManifestPath)
		if info, err := os.Stat(status.ManifestPath); err == nil {
			status.ManifestPresent = true
			status.LastRefreshedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
	case marketplaceSourceKindGit:
		status.CachePath = spec.RepoRoot
		status.ManifestPath = filepath.Join(spec.RepoRoot, marketplaceManifestPath)
		if info, err := os.Stat(filepath.Join(spec.RepoRoot, ".git")); err == nil && info.IsDir() {
			status.Cached = true
		}
		if info, err := os.Stat(status.ManifestPath); err == nil {
			status.ManifestPresent = true
			status.LastRefreshedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		resolved, err := baldagit.GitRunCmdOutput(ctx, spec.RepoRoot, "git", "rev-parse", "HEAD")
		if err == nil {
			status.ResolvedRef = strings.TrimSpace(resolved)
		}
	default:
		return MarketplaceStatus{}, fmt.Errorf("unsupported marketplace source kind")
	}
	if status.ManifestPresent {
		plugins, err := s.readMarketplacePlugins(ctx, src)
		if err == nil {
			status.AvailablePlugins = len(plugins)
		}
	}
	return status, nil
}

func isGitMarketplaceSource(source string) bool {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "://") {
		return true
	}
	if strings.HasPrefix(trimmed, "git@") {
		return true
	}
	return strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "/")
}

func cloneGitMarketplaceSource(ctx context.Context, spec marketplaceSourceSpec) error {
	parent := filepath.Dir(spec.RepoRoot)
	args := []string{"clone"}
	if spec.Ref != "" {
		args = append(args, "--branch", spec.Ref)
	}
	if len(spec.Sparse) > 0 {
		args = append(args, "--no-checkout", "--filter=blob:none")
	}
	args = append(args, spec.GitURL, spec.RepoRoot)
	if err := baldagit.GitRunCmdErr(ctx, parent, "git", args...); err != nil {
		return fmt.Errorf("clone marketplace %q: %w", spec.Name, err)
	}
	if len(spec.Sparse) > 0 {
		if err := configureSparseCheckout(ctx, spec.RepoRoot, spec.Sparse); err != nil {
			return err
		}
		if err := checkoutGitMarketplaceRef(ctx, spec.RepoRoot, spec.Ref); err != nil {
			return err
		}
	}
	return nil
}

func updateGitMarketplaceCheckout(ctx context.Context, spec marketplaceSourceSpec) error {
	if err := baldagit.GitRunCmdErr(ctx, spec.RepoRoot, "git", "fetch", "--all", "--tags", "--prune"); err != nil {
		return fmt.Errorf("fetch marketplace %q: %w", spec.Name, err)
	}
	if len(spec.Sparse) > 0 {
		if err := configureSparseCheckout(ctx, spec.RepoRoot, spec.Sparse); err != nil {
			return err
		}
	}
	if err := checkoutGitMarketplaceRef(ctx, spec.RepoRoot, spec.Ref); err != nil {
		return err
	}
	return nil
}

func configureSparseCheckout(ctx context.Context, repoRoot string, sparse []string) error {
	if err := baldagit.GitRunCmdErr(ctx, repoRoot, "git", "sparse-checkout", "init", "--cone"); err != nil {
		return fmt.Errorf("init sparse checkout: %w", err)
	}
	args := []string{"sparse-checkout", "set"}
	args = append(args, sparse...)
	if err := baldagit.GitRunCmdErr(ctx, repoRoot, "git", args...); err != nil {
		return fmt.Errorf("set sparse checkout: %w", err)
	}
	return nil
}

func checkoutGitMarketplaceRef(ctx context.Context, repoRoot string, ref string) error {
	trimmedRef := strings.TrimSpace(ref)
	if trimmedRef == "" {
		trimmedRef = "origin/HEAD"
	}
	if err := baldagit.GitRunCmdErr(ctx, repoRoot, "git", "checkout", "--force", trimmedRef); err == nil {
		return nil
	}
	if trimmedRef == "origin/HEAD" {
		symbolic, symErr := baldagit.GitRunCmdOutput(ctx, repoRoot, "git", "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if symErr != nil {
			return fmt.Errorf("resolve remote HEAD: %w", symErr)
		}
		resolved := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(symbolic), "refs/remotes/"))
		if resolved == "" {
			return fmt.Errorf("resolve remote HEAD: empty ref")
		}
		if err := baldagit.GitRunCmdErr(ctx, repoRoot, "git", "checkout", "--force", resolved); err != nil {
			return fmt.Errorf("checkout marketplace ref %q: %w", resolved, err)
		}
		return nil
	}
	return fmt.Errorf("checkout marketplace ref %q failed", trimmedRef)
}

func readPluginSummary(root string, manifestPath string) (PluginSummary, error) {
	trimmedManifestPath := strings.TrimSpace(manifestPath)
	candidates := make([]string, 0, 2)
	if trimmedManifestPath != "" {
		candidates = append(candidates, trimmedManifestPath)
	} else {
		candidates = append(candidates, "plugin.json", filepath.Join(".plugin", "plugin.json"))
	}
	var data []byte
	var err error
	for _, candidate := range candidates {
		data, err = os.ReadFile(filepath.Join(root, filepath.Clean(candidate)))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return PluginSummary{}, err
		}
	}
	if err != nil {
		return PluginSummary{}, err
	}
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PluginSummary{}, err
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return PluginSummary{}, fmt.Errorf("plugin name is required")
	}
	return PluginSummary{
		Name:        strings.TrimSpace(manifest.Name),
		Version:     strings.TrimSpace(manifest.Version),
		Description: strings.TrimSpace(manifest.Description),
	}, nil
}

func resolveLocalMarketplaceRoot(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		return ""
	}
	if strings.Contains(trimmed, "/") || strings.HasPrefix(trimmed, ".") {
		if filepath.IsAbs(trimmed) {
			return filepath.Clean(trimmed)
		}
		if abs, err := filepath.Abs(trimmed); err == nil {
			return filepath.Clean(abs)
		}
	}
	return ""
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func rollbackInstalledPlugin(destRoot, backupRoot string) {
	_ = os.RemoveAll(destRoot)
	if _, err := os.Stat(backupRoot); err == nil {
		_ = os.Rename(backupRoot, destRoot)
	}
}
