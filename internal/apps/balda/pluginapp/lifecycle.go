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
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
)

// PluginStore is the lifecycle-owned durable persistence port.
type PluginStore interface {
	PutPluginRevision(ctx context.Context, record baldastate.PluginRevisionRecord) error
	GetPluginRevision(ctx context.Context, pluginID, revisionID string) (baldastate.PluginRevisionRecord, bool, error)
	GetPluginInstall(ctx context.Context, pluginID string) (baldastate.PluginInstallRecord, bool, error)
	ListPluginInstalls(ctx context.Context) ([]baldastate.PluginInstallRecord, error)
	ActivatePlugin(ctx context.Context, intent baldastate.PluginActivationIntent, install baldastate.PluginInstallRecord) error
	DeactivatePlugin(ctx context.Context, intent baldastate.PluginActivationIntent) error
	CompletePluginActivation(ctx context.Context, intentID string, updatedAt time.Time) error
	ListIncompletePluginActivations(ctx context.Context) ([]baldastate.PluginActivationIntent, error)
	CanPurgePluginRevision(ctx context.Context, pluginID, revisionID string) (bool, error)
	PurgePluginRevision(ctx context.Context, pluginID, revisionID string) error
}

// CatalogActivator combines enabled plugin sources with host-owned non-plugin
// sources, compiles the complete application snapshot, and publishes it.
type CatalogActivator interface {
	PreparePluginCandidate(ctx context.Context, enabledPlugins []runtimecatalogcmd.Source) (runtimecatalogcmd.Snapshot, error)
	PublishCandidate(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) error
}

// CapabilitySummary is a stable count-only view used before activation.
type CapabilitySummary struct {
	Commands    int `json:"commands"`
	Skills      int `json:"skills"`
	MCPServers  int `json:"mcp_servers"`
	Diagnostics int `json:"diagnostics"`
}

// CapabilityDiff compares the active and selected marketplace revisions.
type CapabilityDiff struct {
	Active    CapabilitySummary
	Candidate CapabilitySummary
}

type managedLifecycle struct {
	mu        sync.Mutex
	stateDir  string
	store     PluginStore
	activator CatalogActivator
	loader    *runtimecatalog.SourceLoader
	now       func() time.Time
}

func (m *managedLifecycle) install(ctx context.Context, plugin AvailablePlugin) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.reconcilePending(ctx, plugin.Name); err != nil {
		return err
	}
	existing, found, err := m.store.GetPluginInstall(ctx, plugin.Name)
	if err != nil {
		return err
	}
	originPath, err := marketplaceRelativePath(plugin)
	if err != nil {
		return err
	}
	if found && (existing.OriginMarketplace != plugin.Marketplace || existing.OriginSource != plugin.SourceRoot || existing.OriginPath != originPath) {
		return errors.New("plugin origin is locked; change it explicitly before upgrading")
	}
	stagingParent, err := ensureContainedDirectory(m.stateDir, "plugin-staging")
	if err != nil {
		return fmt.Errorf("prepare plugin staging root: %w", err)
	}
	stage, err := os.MkdirTemp(stagingParent, plugin.Name+"-")
	if err != nil {
		return fmt.Errorf("create plugin staging directory: %w", err)
	}
	defer func() {
		_ = makeTreeWritable(stage)
		_ = os.RemoveAll(stage)
	}()
	pluginPackage, err := m.loader.MaterializePlugin(plugin.PluginPath, filepath.Join(stage, "package"))
	if err != nil {
		return fmt.Errorf("validate plugin package: %w", err)
	}
	source := pluginPackage.Source
	if source.Descriptor.ID.Name != plugin.Name {
		return errors.New("marketplace and manifest plugin names differ")
	}
	revisionID := string(source.Descriptor.Revision)
	revisionParent, err := ensureContainedDirectory(m.stateDir, filepath.Join("plugin-revisions", plugin.Name))
	if err != nil {
		return fmt.Errorf("prepare plugin revision root: %w", err)
	}
	finalRoot := filepath.Join(revisionParent, revisionID)
	if info, err := os.Lstat(finalRoot); errors.Is(err, os.ErrNotExist) {
		stagedRoot := filepath.Join(stage, "package")
		if err := os.Chmod(stagedRoot, 0o700); err != nil {
			return fmt.Errorf("prepare plugin revision move: %w", err)
		}
		if err := os.Rename(stagedRoot, finalRoot); err != nil {
			return fmt.Errorf("materialize plugin revision: %w", err)
		}
	} else if err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("plugin revision destination is not a managed directory")
	}
	if err := protectRevisionTree(finalRoot); err != nil {
		return fmt.Errorf("protect plugin revision: %w", err)
	}
	resolvedFinalRoot, err := resolveContainedPath(m.stateDir, filepath.Join("plugin-revisions", plugin.Name, revisionID))
	if err != nil {
		return fmt.Errorf("resolve materialized plugin revision: %w", err)
	}
	if err := m.verifyRevisionRoot(resolvedFinalRoot, revisionID); err != nil {
		return err
	}
	capabilities, err := capabilityJSON(source)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	relativeRoot := filepath.ToSlash(filepath.Join("plugin-revisions", plugin.Name, revisionID))
	revision, revisionFound, err := m.store.GetPluginRevision(ctx, plugin.Name, revisionID)
	if err != nil {
		return err
	}
	if !revisionFound {
		revision = baldastate.PluginRevisionRecord{
			PluginID: plugin.Name, RevisionID: revisionID, Version: pluginPackage.Version,
			Description:  pluginPackage.Description,
			RelativeRoot: relativeRoot, CapabilityJSON: capabilities, CreatedAt: now,
		}
		if err := m.store.PutPluginRevision(ctx, revision); err != nil {
			return err
		}
	} else if revision.RelativeRoot != relativeRoot || revision.CapabilityJSON != capabilities ||
		revision.Version != pluginPackage.Version || revision.Description != pluginPackage.Description {
		return errors.New("plugin revision metadata conflicts with immutable package")
	}
	enabled := true
	fromRevision := ""
	operation := "install"
	dataPath := filepath.ToSlash(filepath.Join("plugin-data", plugin.Name))
	if found {
		enabled = existing.Enabled
		fromRevision = existing.ActiveRevisionID
		operation = "upgrade"
		dataPath = existing.DataRelativePath
		if existing.ActiveRevisionID == revisionID {
			return nil
		}
	}
	if _, err := ensureContainedDirectory(m.stateDir, filepath.FromSlash(dataPath)); err != nil {
		return fmt.Errorf("create plugin data root: %w", err)
	}
	install := baldastate.PluginInstallRecord{PluginID: plugin.Name, OriginMarketplace: plugin.Marketplace, OriginSource: plugin.SourceRoot, OriginPath: originPath, ActiveRevisionID: revisionID, Enabled: enabled, Version: revision.Version, Description: revision.Description, CapabilityJSON: capabilities, DataRelativePath: dataPath, UpdatedAt: now}
	return m.activate(ctx, operation, fromRevision, install, source)
}

func (m *managedLifecycle) verifyRevisionRoot(root, revisionID string) error {
	source, err := m.loader.LoadPlugin(root)
	if err != nil {
		return fmt.Errorf("validate existing plugin revision: %w", err)
	}
	if string(source.Descriptor.Revision) != revisionID {
		return errors.New("existing plugin revision drift detected")
	}
	return nil
}

func (m *managedLifecycle) activate(ctx context.Context, operation, fromRevision string, install baldastate.PluginInstallRecord, proposed runtimecatalogcmd.Source) error {
	sources, err := m.candidateSources(ctx, install, proposed)
	if err != nil {
		return err
	}
	snapshot, err := m.activator.PreparePluginCandidate(ctx, sources)
	if err != nil {
		return fmt.Errorf("compile plugin candidate: %w", err)
	}
	intentID := uuid.NewString()
	intent := baldastate.PluginActivationIntent{IntentID: intentID, PluginID: install.PluginID, FromRevisionID: fromRevision, ToRevisionID: install.ActiveRevisionID, Operation: operation, State: baldastate.PluginActivationIntentPending, CreatedAt: install.UpdatedAt, UpdatedAt: install.UpdatedAt}
	if err := m.store.ActivatePlugin(ctx, intent, install); err != nil {
		return err
	}
	if err := m.activator.PublishCandidate(ctx, snapshot); err != nil {
		return fmt.Errorf("publish plugin candidate: %w", err)
	}
	if err := m.store.CompletePluginActivation(ctx, intentID, m.now().UTC()); err != nil {
		return err
	}
	return nil
}

func (m *managedLifecycle) candidateSources(ctx context.Context, proposedInstall baldastate.PluginInstallRecord, proposed runtimecatalogcmd.Source) ([]runtimecatalogcmd.Source, error) {
	installs, err := m.store.ListPluginInstalls(ctx)
	if err != nil {
		return nil, err
	}
	var sources []runtimecatalogcmd.Source
	seen := false
	for _, install := range installs {
		if install.PluginID == proposedInstall.PluginID {
			seen = true
			if proposedInstall.Enabled {
				sources = append(sources, proposed)
			}
			continue
		}
		if !install.Enabled {
			continue
		}
		source, err := m.loadRevision(ctx, install)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if !seen && proposedInstall.Enabled {
		sources = append(sources, proposed)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Descriptor.ID.String() < sources[j].Descriptor.ID.String() })
	return sources, nil
}

func (m *managedLifecycle) loadRevision(ctx context.Context, install baldastate.PluginInstallRecord) (runtimecatalogcmd.Source, error) {
	revision, found, err := m.store.GetPluginRevision(ctx, install.PluginID, install.ActiveRevisionID)
	if err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	if !found {
		return runtimecatalogcmd.Source{}, errors.New("active plugin revision is unavailable")
	}
	root, err := resolveContainedPath(m.stateDir, filepath.FromSlash(revision.RelativeRoot))
	if err != nil {
		return runtimecatalogcmd.Source{}, fmt.Errorf("resolve active plugin revision: %w", err)
	}
	source, err := m.loader.LoadPlugin(root)
	if err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	if string(source.Descriptor.Revision) != revision.RevisionID {
		return runtimecatalogcmd.Source{}, errors.New("active plugin revision drift detected")
	}
	return source, nil
}

func marketplaceRelativePath(plugin AvailablePlugin) (string, error) {
	root, err := filepath.EvalSymlinks(plugin.SourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve marketplace root: %w", err)
	}
	packageRoot, err := filepath.EvalSymlinks(plugin.PluginPath)
	if err != nil {
		return "", fmt.Errorf("resolve plugin package: %w", err)
	}
	relative, err := filepath.Rel(root, packageRoot)
	if err != nil {
		return "", fmt.Errorf("resolve plugin marketplace path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin package is outside marketplace root")
	}
	return filepath.ToSlash(relative), nil
}

func ensureContainedDirectory(base, relative string) (string, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.ToSlash(relative), 0o700); err != nil {
		return "", err
	}
	return resolveContainedPath(base, relative)
}

func makeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o200)
	})
}

func protectRevisionTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o400)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o500
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i], 0o500); err != nil {
			return err
		}
	}
	return nil
}

func resolveContainedPath(base, relative string) (string, error) {
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(filepath.Join(base, relative))
	if err != nil {
		return "", err
	}
	contained, err := filepath.Rel(resolvedBase, resolvedTarget)
	if err != nil {
		return "", err
	}
	if contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", errors.New("managed plugin path escapes state directory")
	}
	return resolvedTarget, nil
}

func capabilityJSON(source runtimecatalogcmd.Source) (string, error) {
	data, err := json.Marshal(capabilitySummary(source))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func capabilitySummary(source runtimecatalogcmd.Source) CapabilitySummary {
	return CapabilitySummary{
		Commands: len(source.Commands), Skills: len(source.Skills),
		MCPServers: len(source.MCPServers), Diagnostics: len(source.Diagnostics),
	}
}

func (m *managedLifecycle) setEnabled(ctx context.Context, pluginID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.reconcilePending(ctx, pluginID); err != nil {
		return err
	}
	install, found, err := m.store.GetPluginInstall(ctx, pluginID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("plugin not installed")
	}
	if install.Enabled == enabled {
		return nil
	}
	source, err := m.loadRevision(ctx, install)
	if err != nil {
		return err
	}
	install.Enabled = enabled
	install.UpdatedAt = m.now().UTC()
	operation := "enable"
	if !enabled {
		operation = "disable"
	}
	return m.activate(ctx, operation, install.ActiveRevisionID, install, source)
}

func (m *managedLifecycle) rollback(ctx context.Context, pluginID, revisionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.reconcilePending(ctx, pluginID); err != nil {
		return err
	}
	install, found, err := m.store.GetPluginInstall(ctx, pluginID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("plugin not installed")
	}
	if install.ActiveRevisionID == revisionID {
		return nil
	}
	revision, found, err := m.store.GetPluginRevision(ctx, pluginID, revisionID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("plugin revision not found")
	}
	target := install
	target.ActiveRevisionID = revisionID
	target.Version = revision.Version
	target.Description = revision.Description
	target.CapabilityJSON = revision.CapabilityJSON
	target.UpdatedAt = m.now().UTC()
	source, err := m.loadRevision(ctx, target)
	if err != nil {
		return err
	}
	return m.activate(ctx, "rollback", install.ActiveRevisionID, target, source)
}

func (m *managedLifecycle) remove(ctx context.Context, pluginID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	reconciled, err := m.reconcilePending(ctx, pluginID)
	if err != nil {
		return err
	}
	install, found, err := m.store.GetPluginInstall(ctx, pluginID)
	if err != nil {
		return err
	}
	if !found {
		if reconciled {
			return nil
		}
		return errors.New("plugin not installed")
	}
	source, err := m.loadRevision(ctx, install)
	if err != nil {
		return err
	}
	disabled := install
	disabled.Enabled = false
	sources, err := m.candidateSources(ctx, disabled, source)
	if err != nil {
		return err
	}
	snapshot, err := m.activator.PreparePluginCandidate(ctx, sources)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	intentID := uuid.NewString()
	intent := baldastate.PluginActivationIntent{
		IntentID: intentID, PluginID: pluginID, FromRevisionID: install.ActiveRevisionID,
		ToRevisionID: install.ActiveRevisionID, Operation: "remove",
		State: baldastate.PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.DeactivatePlugin(ctx, intent); err != nil {
		return err
	}
	if err := m.activator.PublishCandidate(ctx, snapshot); err != nil {
		return err
	}
	return m.store.CompletePluginActivation(ctx, intentID, m.now().UTC())
}

func (m *managedLifecycle) recover(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoverLocked(ctx)
}

func (m *managedLifecycle) recoverLocked(ctx context.Context) error {
	installs, err := m.store.ListPluginInstalls(ctx)
	if err != nil {
		return err
	}
	var sources []runtimecatalogcmd.Source
	for _, install := range installs {
		if !install.Enabled {
			continue
		}
		source, loadErr := m.loadRevision(ctx, install)
		if loadErr != nil {
			return loadErr
		}
		sources = append(sources, source)
	}
	snapshot, err := m.activator.PreparePluginCandidate(ctx, sources)
	if err != nil {
		return err
	}
	if err := m.activator.PublishCandidate(ctx, snapshot); err != nil {
		return err
	}
	intents, err := m.store.ListIncompletePluginActivations(ctx)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if err := m.store.CompletePluginActivation(ctx, intent.IntentID, m.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (m *managedLifecycle) reconcilePending(ctx context.Context, pluginID string) (bool, error) {
	intents, err := m.store.ListIncompletePluginActivations(ctx)
	if err != nil {
		return false, err
	}
	for _, intent := range intents {
		if intent.PluginID == pluginID {
			return true, m.recoverLocked(ctx)
		}
	}
	return false, nil
}

func (m *managedLifecycle) drifted(ctx context.Context, pluginID string) (bool, error) {
	install, found, err := m.store.GetPluginInstall(ctx, pluginID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("plugin not installed")
	}
	revision, found, err := m.store.GetPluginRevision(ctx, install.PluginID, install.ActiveRevisionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("active plugin revision is unavailable")
	}
	root, err := resolveContainedPath(m.stateDir, filepath.FromSlash(revision.RelativeRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("resolve active plugin revision: %w", err)
	}
	actualRevision, err := m.loader.InspectRevision(root)
	if err != nil {
		return false, fmt.Errorf("inspect active plugin revision: %w", err)
	}
	return string(actualRevision) != revision.RevisionID, nil
}

func (m *managedLifecycle) purge(ctx context.Context, pluginID, revisionID string, purgeData bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.reconcilePending(ctx, pluginID); err != nil {
		return err
	}
	revision, found, err := m.store.GetPluginRevision(ctx, pluginID, revisionID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("plugin revision not found")
	}
	canPurge, err := m.store.CanPurgePluginRevision(ctx, pluginID, revisionID)
	if err != nil {
		return err
	}
	if !canPurge {
		return errors.New("plugin revision is not retired or is still active")
	}
	if purgeData {
		if _, installed, err := m.store.GetPluginInstall(ctx, pluginID); err != nil {
			return err
		} else if installed {
			return errors.New("cannot purge plugin data while installed")
		}
	}
	root, err := resolveContainedPath(m.stateDir, filepath.FromSlash(revision.RelativeRoot))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resolve plugin revision purge target: %w", err)
	}
	trashParent, err := ensureContainedDirectory(m.stateDir, "plugin-purge")
	if err != nil {
		return err
	}
	trashRoot := filepath.Join(trashParent, uuid.NewString())
	moved := false
	if root != "" {
		if err := os.Chmod(root, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prepare plugin revision purge move: %w", err)
		}
	}
	if err := os.Rename(root, trashRoot); err == nil {
		moved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Chmod(root, 0o500)
		return fmt.Errorf("stage plugin revision purge: %w", err)
	}
	if moved {
		if err := makeTreeWritable(trashRoot); err != nil {
			if restoreErr := os.Rename(trashRoot, root); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore plugin revision after failed purge staging: %w", restoreErr))
			}
			_ = os.Chmod(root, 0o500)
			return fmt.Errorf("prepare purged plugin revision deletion: %w", err)
		}
	}
	if err := m.store.PurgePluginRevision(ctx, pluginID, revisionID); err != nil {
		if !moved {
			return err
		}
		if restoreErr := os.Rename(trashRoot, root); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore plugin revision after failed purge: %w", restoreErr))
		}
		_ = os.Chmod(root, 0o500)
		return err
	}
	if err := os.RemoveAll(trashRoot); err != nil {
		return fmt.Errorf("delete purged plugin revision: %w", err)
	}
	if purgeData {
		dataRoot := filepath.Join(m.stateDir, "plugin-data", pluginID)
		if err := os.RemoveAll(dataRoot); err != nil {
			return fmt.Errorf("delete plugin data: %w", err)
		}
	}
	return nil
}

func (m *managedLifecycle) capabilityDiff(
	ctx context.Context,
	plugin AvailablePlugin,
) (CapabilityDiff, error) {
	install, found, err := m.store.GetPluginInstall(ctx, plugin.Name)
	if err != nil {
		return CapabilityDiff{}, err
	}
	if !found {
		return CapabilityDiff{}, errors.New("plugin not installed")
	}
	var active CapabilitySummary
	if err := json.Unmarshal([]byte(install.CapabilityJSON), &active); err != nil {
		return CapabilityDiff{}, fmt.Errorf("decode active plugin capabilities: %w", err)
	}
	candidate, err := m.loader.LoadPlugin(plugin.PluginPath)
	if err != nil {
		return CapabilityDiff{}, err
	}
	return CapabilityDiff{Active: active, Candidate: capabilitySummary(candidate)}, nil
}
