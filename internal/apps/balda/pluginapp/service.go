package pluginapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

type PluginSummary struct {
	Name                   string
	Version                string
	Description            string
	DeclaresBaldaExtension bool
}

type AvailablePlugin struct {
	Revision               string
	Installable            bool
	Stale                  bool
	DeclaresBaldaExtension bool
	capabilities           CapabilitySummary
	marketplaceAlias       string
	Name                   string
	DisplayName            string
	Description            string
	Version                string
	Marketplace            string
	MarketplaceLabel       string
	Category               string
	SourceRoot             string
	PluginPath             string
	Installed              bool
	Diagnostics            []runtimecatalogcmd.Diagnostic
}

type Service struct {
	stateDir   string
	kv         baldastate.KVStore
	managed    *managedLifecycle
	management ManagementStore
}

// InstalledState is the application-owned durable plugin state exposed only
// to composition adapters. It contains logical references, never host paths.
type InstalledState struct {
	Name         string
	Version      string
	Marketplace  string
	Origin       string
	Revision     string
	Enabled      bool
	Drifted      bool
	Capabilities CapabilitySummary
}

type MarketplaceSource struct {
	Name   string
	Source string
	Ref    string
	Sparse []string
}

type MarketplaceStatus struct {
	LastAttemptAt     string
	LastAttemptStatus string
	FailureCode       string
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

func newService(stateDir string, kv baldastate.KVStore) (*Service, error) {
	trimmed := strings.TrimSpace(stateDir)
	if trimmed == "" {
		return nil, fmt.Errorf("state dir is required")
	}
	return &Service{stateDir: trimmed, kv: kv}, nil
}

// NewManaged creates the durable plugin lifecycle service.
func NewManaged(stateDir string, kv baldastate.KVStore, store PluginStore, activator CatalogActivator, management ManagementStore) (*Service, error) {
	if management == nil {
		return nil, fmt.Errorf("management store is required")
	}
	service, err := newService(stateDir, kv)
	if err != nil {
		return nil, err
	}
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		return nil, err
	}
	if store == nil || activator == nil {
		return nil, fmt.Errorf("plugin store and catalog activator are required")
	}
	if err := os.MkdirAll(service.stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin state directory: %w", err)
	}
	service.management = management
	service.managed = &managedLifecycle{stateDir: service.stateDir, store: store, kv: kv, activator: activator, loader: loader, now: time.Now, management: service.management}
	return service, nil
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
	installs, err := s.managed.store.ListPluginInstalls(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PluginSummary, 0, len(installs))
	for _, install := range installs {
		out = append(out, PluginSummary{Name: install.PluginID, Version: install.Version, Description: install.Description})
	}
	return out, nil
}

func (s *Service) GetInstalled(ctx context.Context, name string) (PluginSummary, bool, error) {
	install, found, err := s.managed.store.GetPluginInstall(ctx, strings.TrimSpace(name))
	if err != nil || !found {
		return PluginSummary{}, found, err
	}
	return PluginSummary{Name: install.PluginID, Version: install.Version, Description: install.Description}, true, nil
}

const marketplacePrefix = "plugin_marketplace:"
const marketplaceManifestPath = ".agents/plugins/marketplace.json"
const supportedPluginManifestSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

type marketplaceIndex struct {
	Name      string `json:"name"`
	Interface struct {
		DisplayName string `json:"displayName"`
	} `json:"interface"`
	Plugins []marketplacePlugin `json:"plugins"`
}

type marketplacePlugin struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	} `json:"source"`
	ManifestPath string `json:"manifest_path"`
	Category     string `json:"category"`
}

func (p marketplacePlugin) packagePath() (string, bool) {
	if path := strings.TrimSpace(p.Path); path != "" {
		return path, false
	}
	if strings.TrimSpace(p.Source.Source) != string(marketplaceSourceKindLocal) {
		return "", false
	}
	return strings.TrimSpace(p.Source.Path), true
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
	unlock := s.lockOwner()
	defer unlock()
	return s.marketplaceStatusesLocked(ctx)
}

func (s *Service) GetMarketplaceStatus(ctx context.Context, name string) (MarketplaceStatus, bool, error) {
	unlock := s.lockOwner()
	defer unlock()
	detail, err := s.marketplaceDetailLocked(ctx, strings.TrimSpace(name))
	if errors.Is(err, plugincmd.ErrNotFound) {
		return MarketplaceStatus{}, false, nil
	}
	if err != nil {
		return MarketplaceStatus{}, false, err
	}
	return marketplaceStatusView(detail), true, nil
}

func (s *Service) AddMarketplace(ctx context.Context, src MarketplaceSource) error {
	return s.addMarketplaceTrusted(ctx, src)
}

func (s *Service) RemoveMarketplace(ctx context.Context, name string) error {
	return s.removeMarketplaceTrusted(ctx, name)
}

func (s *Service) UpgradeMarketplaces(ctx context.Context, name string) ([]MarketplaceUpgradeResult, error) {
	return s.refreshMarketplacesTrusted(ctx, name)
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
	unlock := s.lockOwner()
	defer unlock()
	return s.availableLocked(ctx)
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
		if marketplace != "" && plugin.Marketplace != marketplace && plugin.marketplaceAlias != marketplace {
			continue
		}
		if found {
			return AvailablePlugin{}, false, fmt.Errorf("%w: multiple marketplace matches for plugin %q", plugincmd.ErrConflict, name)
		}
		match = plugin
		found = true
	}
	return match, found, nil
}

func (s *Service) Install(ctx context.Context, selector string) error {
	return s.mutatePluginTrusted(ctx, selector, "install")
}

func (s *Service) RemoveInstalled(ctx context.Context, name string) error {
	return s.managed.remove(ctx, strings.TrimSpace(name))
}

func (s *Service) Enable(ctx context.Context, name string) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.setEnabled(ctx, strings.TrimSpace(name), true)
}

// Upgrade validates and atomically upgrades an existing managed installation.
func (s *Service) Upgrade(ctx context.Context, selector string) error {
	return s.mutatePluginTrusted(ctx, selector, string(plugincmd.Upgrade))
}

// AdoptOrigin explicitly locks an origin-unknown migrated install to one
// unambiguous marketplace package before its first managed upgrade.
func (s *Service) AdoptOrigin(ctx context.Context, selector string) error {
	return s.mutatePluginTrusted(ctx, selector, pluginAdoptOrigin)
}

func (s *Service) Disable(ctx context.Context, name string) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.setEnabled(ctx, strings.TrimSpace(name), false)
}

func (s *Service) Rollback(ctx context.Context, name, revision string) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.rollback(ctx, strings.TrimSpace(name), strings.TrimSpace(revision))
}

func (s *Service) Recover(ctx context.Context) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.recover(ctx)
}

// MigrateLegacy imports startup-era packages as origin-unknown managed
// revisions. Their origin must be selected explicitly before upgrade.
func (s *Service) MigrateLegacy(ctx context.Context) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.migrateLegacy(ctx)
}

func (s *Service) Drifted(ctx context.Context, name string) (bool, error) {
	if s.managed == nil {
		return false, errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.drifted(ctx, strings.TrimSpace(name))
}

func (s *Service) Purge(ctx context.Context, name, revision string, purgeData bool) error {
	if s.managed == nil {
		return errors.New("managed plugin lifecycle is unavailable")
	}
	return s.managed.purge(ctx, strings.TrimSpace(name), strings.TrimSpace(revision), purgeData)
}

// Capabilities compares the installed revision with an available marketplace package.
func (s *Service) Capabilities(ctx context.Context, selector string) (CapabilityDiff, error) {
	if s.managed == nil {
		return CapabilityDiff{}, errors.New("managed plugin lifecycle is unavailable")
	}
	plugin, found, err := s.GetAvailable(ctx, selector)
	if err != nil {
		return CapabilityDiff{}, err
	}
	if !found {
		return CapabilityDiff{}, errors.New("plugin not found")
	}
	install, installed, err := s.managed.store.GetPluginInstall(ctx, plugin.Name)
	if err != nil {
		return CapabilityDiff{}, err
	}
	if !installed {
		return CapabilityDiff{}, plugincmd.ErrNotFound
	}
	var active CapabilitySummary
	if err := json.Unmarshal([]byte(install.CapabilityJSON), &active); err != nil {
		return CapabilityDiff{}, plugincmd.ErrUnavailable
	}
	return CapabilityDiff{Active: active, Candidate: plugin.capabilities}, nil
}

// Inspect returns bounded durable state for one managed installation.
func (s *Service) Inspect(ctx context.Context, name string) (InstalledState, bool, error) {
	if s.managed == nil {
		return InstalledState{}, false, errors.New("managed plugin lifecycle is unavailable")
	}
	install, found, err := s.managed.store.GetPluginInstall(ctx, strings.TrimSpace(name))
	if err != nil || !found {
		return InstalledState{}, found, err
	}
	var capabilities CapabilitySummary
	if err := json.Unmarshal([]byte(install.CapabilityJSON), &capabilities); err != nil {
		return InstalledState{}, false, fmt.Errorf("decode plugin capabilities: %w", err)
	}
	drifted, err := s.managed.drifted(ctx, install.PluginID)
	if err != nil {
		return InstalledState{}, false, err
	}
	return InstalledState{
		Name: install.PluginID, Version: install.Version, Marketplace: install.OriginMarketplace,
		Origin: install.OriginPath, Revision: install.ActiveRevisionID, Enabled: install.Enabled,
		Drifted: drifted, Capabilities: capabilities,
	}, true, nil
}

func splitSelector(selector string) (name string, marketplace string) {
	parts := strings.SplitN(strings.TrimSpace(selector), "@", 2)
	name = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		marketplace = strings.TrimSpace(parts[1])
	}
	return name, marketplace
}

func containedMarketplacePath(root, relative string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(relative)))
	if err != nil {
		return "", err
	}
	relativeToRoot, err := filepath.Rel(root, path)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin path escapes marketplace root")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("plugin path is not a directory")
	}
	return path, nil
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
			remote, remoteErr := marketplaceGitOutput(ctx, spec.RepoRoot, "remote", "get-url", "origin")
			if remoteErr == nil && strings.TrimSpace(remote) == spec.GitURL {
				if err := updateGitMarketplaceCheckout(ctx, spec); err != nil {
					return "", err
				}
				return spec.RepoRoot, nil
			}
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
	if err := marketplaceGitRun(ctx, parent, args...); err != nil {
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
	if err := marketplaceGitRun(ctx, spec.RepoRoot, "fetch", "--all", "--tags", "--prune"); err != nil {
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
	if err := marketplaceGitRun(ctx, repoRoot, "sparse-checkout", "init", "--cone"); err != nil {
		return fmt.Errorf("init sparse checkout: %w", err)
	}
	args := []string{"sparse-checkout", "set"}
	args = append(args, sparse...)
	if err := marketplaceGitRun(ctx, repoRoot, args...); err != nil {
		return fmt.Errorf("set sparse checkout: %w", err)
	}
	return nil
}

func checkoutGitMarketplaceRef(ctx context.Context, repoRoot string, ref string) error {
	trimmedRef := strings.TrimSpace(ref)
	if trimmedRef == "" {
		trimmedRef = "origin/HEAD"
	} else {
		remoteRef := "refs/remotes/origin/" + trimmedRef
		if resolved, err := marketplaceGitOutput(ctx, repoRoot, "rev-parse", "--verify", remoteRef); err == nil {
			trimmedRef = strings.TrimSpace(resolved)
		}
	}
	if err := marketplaceGitRun(ctx, repoRoot, "checkout", "--force", trimmedRef); err == nil {
		return nil
	}
	if trimmedRef == "origin/HEAD" {
		symbolic, symErr := marketplaceGitOutput(ctx, repoRoot, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if symErr != nil {
			return fmt.Errorf("resolve remote HEAD: %w", symErr)
		}
		resolved := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(symbolic), "refs/remotes/"))
		if resolved == "" {
			return fmt.Errorf("resolve remote HEAD: empty ref")
		}
		if err := marketplaceGitRun(ctx, repoRoot, "checkout", "--force", resolved); err != nil {
			return fmt.Errorf("checkout marketplace ref %q: %w", resolved, err)
		}
		return nil
	}
	return fmt.Errorf("checkout marketplace ref %q failed", trimmedRef)
}

func readPluginSummary(root string, manifestPath string) (PluginSummary, error) {
	trimmedManifestPath := strings.TrimSpace(manifestPath)
	candidates := make([]string, 0, 1)
	if trimmedManifestPath != "" {
		candidates = append(candidates, trimmedManifestPath)
	} else {
		candidates = append(candidates, "plugin.json")
	}
	var data []byte
	var err error
	for _, candidate := range candidates {
		var manifestFile string
		manifestFile, err = containedMarketplaceFile(root, candidate)
		if err != nil {
			return PluginSummary{}, err
		}
		data, err = readBoundedMarketplaceFile(manifestFile)
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
		Schema      string          `json:"$schema"`
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Extensions  json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PluginSummary{}, err
	}
	if strings.TrimSpace(manifest.Schema) != supportedPluginManifestSchema {
		return PluginSummary{}, fmt.Errorf("unsupported plugin schema")
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return PluginSummary{}, fmt.Errorf("plugin name is required")
	}
	var extensions map[string]json.RawMessage
	if err := json.Unmarshal(manifest.Extensions, &extensions); err != nil {
		extensions = nil
	}
	_, declaresBaldaExtension := extensions["dev.baldaworks.balda"]
	return PluginSummary{
		Name:                   strings.TrimSpace(manifest.Name),
		Version:                strings.TrimSpace(manifest.Version),
		Description:            strings.TrimSpace(manifest.Description),
		DeclaresBaldaExtension: declaresBaldaExtension,
	}, nil
}

func containedMarketplaceFile(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("manifest path must be relative")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, filepath.Clean(relative)))
	if err != nil {
		return "", err
	}
	relativeToRoot, err := filepath.Rel(resolvedRoot, path)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("manifest path escapes plugin root")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("manifest path is not a regular file")
	}
	return path, nil
}

func resolveLocalMarketplaceRoot(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") || scpMarketplaceSource(trimmed) {
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
