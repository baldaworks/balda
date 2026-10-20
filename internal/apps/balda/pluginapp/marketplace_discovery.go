package pluginapp

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

const maxMarketplaceIndexBytes = 4 << 20

func (s *Service) validateManagedMarketplace(src MarketplaceSource) error {
	if !safeMarketplaceName(src.Name) || src.Source == "" || strings.ContainsAny(src.Source, "\x00\r\n") || strings.HasPrefix(src.Source, "-") {
		return plugincmd.ErrInvalid
	}
	if len(src.Ref) > 256 || strings.HasPrefix(src.Ref, "-") || strings.ContainsAny(src.Ref, "\x00\r\n") {
		return plugincmd.ErrInvalid
	}
	if len(src.Sparse) > 128 {
		return plugincmd.ErrInvalid
	}
	for _, path := range src.Sparse {
		clean := filepath.ToSlash(filepath.Clean(path))
		if len(path) > 1024 || path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "-") || strings.ContainsAny(path, "\\\x00\r\n") || clean == ".." || strings.HasPrefix(clean, "../") {
			return plugincmd.ErrInvalid
		}
	}
	if resolveLocalMarketplaceRoot(src.Source) != "" {
		if src.Ref != "" || len(src.Sparse) > 0 {
			return plugincmd.ErrInvalid
		}
		return nil
	}
	if strings.Contains(src.Source, "://") {
		parsed, err := url.Parse(src.Source)
		if err != nil {
			return plugincmd.ErrInvalid
		}
		switch parsed.Scheme {
		case "https", "http", "git", "ssh":
			if parsed.Host == "" {
				return plugincmd.ErrInvalid
			}
		case "file":
			if !filepath.IsAbs(parsed.Path) || (parsed.Host != "" && parsed.Host != "localhost") {
				return plugincmd.ErrInvalid
			}
		default:
			return plugincmd.ErrInvalid
		}
		return nil
	}
	if scpMarketplaceSource(src.Source) {
		return nil
	}
	return plugincmd.ErrInvalid
}

func scpMarketplaceSource(source string) bool {
	if strings.Contains(source, "://") {
		return false
	}
	host, path, ok := strings.Cut(source, ":")
	if len(host) == 1 && ((host[0] >= 'A' && host[0] <= 'Z') || (host[0] >= 'a' && host[0] <= 'z')) {
		return false
	}
	return ok && host != "" && path != "" && !strings.ContainsAny(host, "/\\ \t") && !strings.Contains(host, ":")
}

func safeMarketplaceSource(source string, kind marketplaceSourceKind) string {
	if kind == marketplaceSourceKindLocal {
		return string(marketplaceSourceKindLocal)
	}
	if scpMarketplaceSource(source) {
		host, path, _ := strings.Cut(source, ":")
		if _, after, ok := strings.Cut(host, "@"); ok {
			host = after
		}
		parsed, err := url.Parse("ssh://" + host + "/" + path)
		if err != nil {
			return "redacted"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	return plugincmd.PublicMarketplaceSource(source)
}

func (s *Service) discoverMarketplaceLocked(ctx context.Context, src MarketplaceSource) (*discoverySuccess, string) {
	root, err := s.materializeMarketplaceSource(ctx, src)
	if err != nil {
		return nil, discoveryUnavailable
	}
	// Candidate roots are canonicalized by containedMarketplacePath. Compute
	// relative paths against the same root, including aliased macOS temp dirs.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, discoveryUnavailable
	}
	secure, err := os.OpenRoot(root)
	if err != nil {
		return nil, discoveryUnavailable
	}
	defer func() { _ = secure.Close() }()
	f, err := secure.Open(marketplaceManifestPath)
	if err != nil {
		return nil, discoveryInvalidIndex
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxMarketplaceIndexBytes+1))
	_ = f.Close()
	if readErr != nil || len(data) > maxMarketplaceIndexBytes {
		return nil, discoveryInvalidIndex
	}
	var fields map[string]json.RawMessage
	var index marketplaceIndex
	if json.Unmarshal(data, &fields) != nil || fields == nil || json.Unmarshal(data, &index) != nil || index.Plugins == nil {
		return nil, discoveryInvalidIndex
	}
	if len(index.Plugins) > runtimecatalog.DefaultSourceLimits().MaxEntries {
		return nil, discoveryInvalidIndex
	}
	if duplicateMarketplaceNames(index.Plugins) {
		return nil, discoveryDuplicateName
	}
	success := &discoverySuccess{IndexName: strings.TrimSpace(index.Name)}
	if success.IndexName == "" {
		success.IndexName = src.Name
	}
	loader := s.managed.loader
	invalid := false
	manifestNames := make(map[string]bool, len(index.Plugins))
	for _, entry := range index.Plugins {
		name := strings.TrimSpace(entry.Name)
		packagePath, transitional := entry.packagePath()
		if packagePath == "" || filepath.IsAbs(packagePath) {
			invalid = true
			continue
		}
		pluginRoot, err := containedMarketplacePath(root, packagePath)
		if err != nil {
			invalid = true
			continue
		}
		summary, err := readPluginSummary(pluginRoot, entry.ManifestPath)
		if err != nil {
			invalid = true
			continue
		}
		identity := strings.ToLower(strings.TrimSpace(summary.Name))
		if manifestNames[identity] {
			return nil, discoveryDuplicateName
		}
		manifestNames[identity] = true
		if name == "" || summary.Name != name {
			invalid = true
			continue
		}
		pkg, packageErr := loader.LoadPluginPackage(pluginRoot)
		revision := pkg.Source.Descriptor.Revision
		if packageErr != nil {
			revision, err = loader.InspectRevision(pluginRoot)
			if err != nil {
				invalid = true
				continue
			}
		}
		relative, err := filepath.Rel(root, pluginRoot)
		if err != nil {
			invalid = true
			continue
		}
		candidate := discoveryCandidate{Category: strings.TrimSpace(entry.Category), Name: name, Version: summary.Version, Description: summary.Description, RelativePath: filepath.ToSlash(relative), ManifestPath: strings.TrimSpace(entry.ManifestPath), Revision: string(revision), DeclaresBaldaExtension: summary.DeclaresBaldaExtension}
		if packageErr == nil && pkg.Version == summary.Version && pkg.Source.Descriptor.ID.Name == name {
			candidate.Installable = true
			c := capabilitySummary(pkg.Source)
			candidate.Capabilities = plugincmd.CapabilitySummary{Commands: c.Commands, Skills: c.Skills, MCPServers: c.MCPServers, Diagnostics: c.Diagnostics}
			for _, d := range pkg.Source.Diagnostics {
				if d.Severity == runtimecatalogcmd.DiagnosticSeverityError {
					candidate.Installable = false
				}
				candidate.DiagnosticCodes = append(candidate.DiagnosticCodes, d.Code)
			}
		} else {
			candidate.DiagnosticCodes = append(candidate.DiagnosticCodes, "package_validation_failed")
		}
		if transitional {
			candidate.DiagnosticCodes = append(candidate.DiagnosticCodes, runtimecatalogcmd.DiagnosticMarketplaceSourcePathDeprecated)
		}
		success.Candidates = append(success.Candidates, candidate)
	}
	if invalid {
		success.DiagnosticCodes = []string{discoveryInvalidEntry}
	}
	if resolveLocalMarketplaceRoot(src.Source) == "" {
		head, err := marketplaceGitOutput(ctx, root, "rev-parse", "HEAD")
		if err != nil {
			return nil, discoveryUnavailable
		}
		success.ResolvedRevision = strings.TrimSpace(head)
	} else {
		raw, _ := json.Marshal(success.Candidates)
		success.ResolvedRevision = plugincmd.Digest(string(data) + string(raw))
	}
	return success, ""
}

// acceptedCandidateLocked returns only current successful, explicitly reviewed
// discovery. The lifecycle must rematerialize and compare Revision before commit.
func (s *Service) acceptedCandidateLocked(ctx context.Context, marketplace, name, token string) (state.MarketplaceObservation, MarketplaceSource, discoveryCandidate, error) {
	observed, src, metadata, err := s.marketplaceStateLocked(ctx, marketplace)
	if err != nil {
		return observed, src, discoveryCandidate{}, err
	}
	if metadata.LastAttemptStatus != discoverySucceeded || metadata.LastSuccessful == nil {
		return observed, src, discoveryCandidate{}, plugincmd.ErrConflict
	}
	for _, candidate := range metadata.LastSuccessful.Candidates {
		if candidate.Name == name {
			if !candidate.Installable {
				return observed, src, discoveryCandidate{}, plugincmd.ErrUnavailable
			}
			if token == "" || discoveryCandidateToken(observed, candidate) != token {
				return observed, src, discoveryCandidate{}, plugincmd.ErrConflict
			}
			return observed, src, candidate, nil
		}
	}
	return observed, src, discoveryCandidate{}, plugincmd.ErrNotFound
}

func validDiscoveryMetadata(metadata discoverySnapshot) bool {
	switch metadata.LastAttemptStatus {
	case "":
		return metadata.LastAttemptAt.IsZero() && metadata.FailureCode == "" && metadata.LastSuccessful == nil
	case discoverySucceeded:
		if metadata.LastAttemptAt.IsZero() || metadata.FailureCode != "" || metadata.LastSuccessful == nil || !metadata.LastSuccessful.At.Equal(metadata.LastAttemptAt) {
			return false
		}
	case discoveryFailed:
		if metadata.LastAttemptAt.IsZero() {
			return false
		}
		switch metadata.FailureCode {
		case discoveryUnavailable, discoveryInvalidIndex, discoveryDuplicateName:
		default:
			return false
		}
	default:
		return false
	}
	success := metadata.LastSuccessful
	if success == nil {
		return true
	}
	if success.At.IsZero() || success.At.After(metadata.LastAttemptAt) || success.ResolvedRevision == "" || success.IndexName == "" || len(success.Candidates) > runtimecatalog.DefaultSourceLimits().MaxEntries {
		return false
	}
	seen := make(map[string]bool, len(success.Candidates))
	for _, candidate := range success.Candidates {
		if candidate.Name == "" || candidate.Name != strings.TrimSpace(candidate.Name) || seen[candidate.Name] || candidate.Revision == "" || candidate.RelativePath == "" || filepath.IsAbs(candidate.RelativePath) {
			return false
		}
		relative := filepath.ToSlash(filepath.Clean(candidate.RelativePath))
		if relative == ".." || strings.HasPrefix(relative, "../") {
			return false
		}
		seen[candidate.Name] = true
	}
	return true
}

func readBoundedMarketplaceFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxMarketplaceIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMarketplaceIndexBytes {
		return nil, plugincmd.ErrUnavailable
	}
	return data, nil
}

// Duplicate identities must be rejected before any invalid-entry filtering.
func duplicateMarketplaceNames(entries []marketplacePlugin) bool {
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			continue
		}
		if names[name] {
			return true
		}
		names[name] = true
	}
	return false
}
