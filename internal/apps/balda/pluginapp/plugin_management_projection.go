package pluginapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// ManagementInventory reads durable installations without refreshing sources.
func (s *Service) ManagementInventory(ctx context.Context, a plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return nil, err
	}
	unlock := s.lockOwner()
	defer unlock()
	if err := s.managementAuthority(ctx, a); err != nil {
		return nil, err
	}
	installs, err := s.managed.store.ListPluginInstalls(ctx)
	if err != nil {
		return nil, plugincmd.ErrUnavailable
	}
	result := make([]plugincmd.InstalledView, 0, len(installs))
	for _, install := range installs {
		observed, token, err := s.pluginObservationLocked(ctx, install.PluginID)
		if err != nil {
			return nil, boundedPluginError(err)
		}
		if observed.Install == nil {
			return nil, plugincmd.ErrConflict
		}
		view, err := s.installedViewLocked(ctx, observed, token)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// ManagementDetail inspects retained content even when its plugin is disabled.
func (s *Service) ManagementDetail(ctx context.Context, name string, a plugincmd.Authority) (plugincmd.PluginDetail, error) {
	if err := s.managementAuthority(ctx, a); err != nil {
		return plugincmd.PluginDetail{}, err
	}
	unlock := s.lockOwner()
	defer unlock()
	if err := s.managementAuthority(ctx, a); err != nil {
		return plugincmd.PluginDetail{}, err
	}
	observed, token, err := s.pluginObservationLocked(ctx, name)
	if err != nil {
		return plugincmd.PluginDetail{}, boundedPluginError(err)
	}
	sourceInstall := observed.Install
	var view plugincmd.InstalledView
	if sourceInstall == nil {
		// A committed remove may still await publication. Reopen can inspect its
		// retained package without representing it as an installed selection.
		for _, intent := range observed.Pending {
			if intent.Operation != "remove" {
				continue
			}
			for _, revision := range observed.Revisions {
				if revision.RevisionID != intent.FromRevisionID {
					continue
				}
				sourceInstall = &state.PluginInstallRecord{PluginID: name, ActiveRevisionID: revision.RevisionID, Version: revision.Version, CapabilityJSON: revision.CapabilityJSON}
			}
		}
		if sourceInstall == nil {
			return plugincmd.PluginDetail{}, plugincmd.ErrNotFound
		}
		view = plugincmd.InstalledView{Removed: true, RecoveryPending: true, ExpectedState: token, PluginStatus: plugincmd.PluginStatus{Name: name, Version: sourceInstall.Version, Revision: sourceInstall.ActiveRevisionID}}
		if json.Unmarshal([]byte(sourceInstall.CapabilityJSON), &view.Capabilities) != nil {
			return plugincmd.PluginDetail{}, plugincmd.ErrUnavailable
		}
	} else {
		view, err = s.installedViewLocked(ctx, observed, token)
		if err != nil {
			return plugincmd.PluginDetail{}, err
		}
	}
	detail := plugincmd.PluginDetail{InstalledView: view}

	for _, revision := range observed.Revisions {
		root, err := resolveContainedPath(s.stateDir, filepath.FromSlash(revision.RelativeRoot))
		available := false
		if err == nil {
			actual, err := s.managed.loader.InspectRevision(root)
			available = err == nil && string(actual) == revision.RevisionID
		}
		detail.Revisions = append(detail.Revisions, plugincmd.RetainedRevision{Version: revision.Version, Revision: revision.RevisionID, RetainedAt: revision.CreatedAt, Available: available, Current: !view.Removed && revision.RevisionID == view.Revision})
	}
	source, err := s.managed.loadRevision(ctx, *sourceInstall)
	if err != nil {
		detail.DeclaresBaldaExtension = false
		detail.DiagnosticCodes = append(detail.DiagnosticCodes, "revision_unavailable")
		return detail, nil
	}
	for _, command := range source.Commands {
		detail.Commands = append(detail.Commands, plugincmd.Contribution{Name: command.Name, Description: command.Description})
	}
	for _, skill := range source.Skills {
		detail.Skills = append(detail.Skills, plugincmd.Contribution{Name: skill.Name, Description: skill.Description})
	}
	for _, server := range source.MCPServers {
		detail.MCPServers = append(detail.MCPServers, plugincmd.Contribution{Name: server.Name, Transport: server.Transport})
	}
	for _, diagnostic := range source.Diagnostics {
		detail.DiagnosticCodes = append(detail.DiagnosticCodes, diagnostic.Code)
	}
	sort.Slice(detail.Commands, func(i, j int) bool { return detail.Commands[i].Name < detail.Commands[j].Name })
	sort.Slice(detail.Skills, func(i, j int) bool { return detail.Skills[i].Name < detail.Skills[j].Name })
	sort.Slice(detail.MCPServers, func(i, j int) bool { return detail.MCPServers[i].Name < detail.MCPServers[j].Name })
	sort.Strings(detail.DiagnosticCodes)
	return detail, nil
}

func (s *Service) installedViewLocked(ctx context.Context, observed state.PluginObservation, token string) (plugincmd.InstalledView, error) {
	install := observed.Install
	var capabilities plugincmd.CapabilitySummary
	if json.Unmarshal([]byte(install.CapabilityJSON), &capabilities) != nil {
		return plugincmd.InstalledView{}, plugincmd.ErrUnavailable
	}
	view := plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: install.PluginID, Version: install.Version, Marketplace: install.OriginMarketplace, Revision: install.ActiveRevisionID, Enabled: install.Enabled, Capabilities: capabilities}, ExpectedState: token, RecoveryPending: len(observed.Pending) > 0}
	// Origin describes proof availability, never the private package path.
	view.Origin = "Locked"
	if install.OriginMarketplace == originUnknown {
		view.Origin = "Unverified"
	}
	selected, err := s.resolveManagementCandidateLocked(ctx, observed, "", false)
	if err == nil {
		_, root, err := s.candidateIdentity(selected.source, selected.candidate)
		if err == nil {
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(selected.candidate.RelativePath)))
			view.OriginAvailable = err == nil && info.IsDir()
		}
	}
	if pkg, err := s.managed.loadRevisionPackage(ctx, *install); err != nil {
		view.Drifted = true
	} else {
		view.DeclaresBaldaExtension = pkg.DeclaresBaldaExtension
	}
	return view, nil
}
