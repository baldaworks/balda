package pluginapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type selectedPluginCandidate struct {
	observation    state.MarketplaceObservation
	source         MarketplaceSource
	candidate      discoveryCandidate
	originIdentity string
}

func (s *Service) candidateIdentity(src MarketplaceSource, c discoveryCandidate) (string, string, error) {
	spec, err := s.marketplaceSourceSpec(src)
	if err != nil {
		return "", "", plugincmd.ErrUnavailable
	}
	root := spec.LocalRoot
	identity := spec.GitURL
	if spec.Kind == marketplaceSourceKindGit {
		root = spec.RepoRoot
	} else {
		identity = root
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", plugincmd.ErrUnavailable
	}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	if spec.Kind == marketplaceSourceKindLocal {
		identity = root
	}
	data, _ := json.Marshal([]string{string(spec.Kind), src.Name, identity, filepath.ToSlash(c.RelativePath)})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), root, nil
}

func (s *Service) resolveManagementCandidateLocked(ctx context.Context, observed state.PluginObservation, marketplace string, allowManifestName bool) (selectedPluginCandidate, error) {
	sources, err := s.ListMarketplaces(ctx)
	if err != nil {
		return selectedPluginCandidate{}, plugincmd.ErrUnavailable
	}
	var selected selectedPluginCandidate
	count := 0
	for _, src := range sources {
		record, _, metadata, err := s.marketplaceStateLocked(ctx, src.Name)
		if err != nil {
			return selectedPluginCandidate{}, err
		}
		if metadata.LastAttemptStatus != discoverySucceeded || metadata.LastSuccessful == nil {
			continue
		}
		if marketplace != "" && src.Name != marketplace && (!allowManifestName || metadata.LastSuccessful.IndexName != marketplace) {
			continue
		}
		for _, c := range metadata.LastSuccessful.Candidates {
			if c.Name != observed.PluginID || !c.Installable {
				continue
			}
			identity, root, err := s.candidateIdentity(src, c)
			if err != nil {
				return selectedPluginCandidate{}, err
			}
			if observed.Install != nil {
				install := observed.Install
				if install.OriginMarketplace != src.Name && install.OriginMarketplace != metadata.LastSuccessful.IndexName {
					continue
				}
				installedRoot := install.OriginSource
				if canonical, err := filepath.EvalSymlinks(installedRoot); err == nil {
					installedRoot = canonical
				}
				if installedRoot != root || filepath.ToSlash(install.OriginPath) != filepath.ToSlash(c.RelativePath) {
					continue
				}
				spec, err := s.marketplaceSourceSpec(src)
				if err != nil {
					return selectedPluginCandidate{}, plugincmd.ErrUnavailable
				}
				if observed.OriginIdentity != "" {
					if observed.OriginIdentity != identity {
						continue
					}
				} else if spec.Kind != marketplaceSourceKindLocal {
					continue
				}
			}
			selected = selectedPluginCandidate{observation: record, source: src, candidate: c, originIdentity: identity}
			count++
		}
	}
	if count != 1 {
		return selectedPluginCandidate{}, plugincmd.ErrConflict
	}
	return selected, nil
}

func (s *Service) materializeAcceptedCandidateLocked(ctx context.Context, selected selectedPluginCandidate, installed *state.PluginInstallRecord) (AvailablePlugin, error) {
	root, err := s.materializeMarketplaceSource(ctx, selected.source)
	if err != nil {
		return AvailablePlugin{}, plugincmd.ErrUnavailable
	}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	} else {
		return AvailablePlugin{}, plugincmd.ErrUnavailable
	}
	marketplace := selected.source.Name
	if installed != nil {
		installedRoot, err := filepath.EvalSymlinks(installed.OriginSource)
		if err != nil || root != installedRoot {
			return AvailablePlugin{}, plugincmd.ErrConflict
		}
		root = installed.OriginSource
		marketplace = installed.OriginMarketplace
	}
	path, err := containedMarketplacePath(root, selected.candidate.RelativePath)
	if err != nil {
		return AvailablePlugin{}, plugincmd.ErrUnavailable
	}
	return AvailablePlugin{Name: selected.candidate.Name, Version: selected.candidate.Version, Description: selected.candidate.Description, Marketplace: marketplace, SourceRoot: root, PluginPath: path}, nil
}
