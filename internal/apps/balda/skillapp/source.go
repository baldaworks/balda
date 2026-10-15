package skillapp

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/google/uuid"
)

const (
	sourceLocal    = "local"
	sourceGit      = "git"
	fileScheme     = "file"
	gitSourceLabel = "Git repository"
)

func sameOrigin(a, b skillcmd.Origin) bool {
	return a.Kind == b.Kind && a.Source == b.Source && a.Path == b.Path
}

func normalizeOrigin(request InstallRequest) (skillcmd.Origin, error) {
	source := strings.TrimSpace(request.Source)
	relative := strings.TrimSpace(request.Path)
	ref := strings.TrimSpace(request.Ref)
	if relative == "" {
		relative = "."
	}
	if source == "" || len(source) > 8192 || len(ref) > 1024 || len(relative) > 4096 || strings.ContainsAny(source+relative+ref, "\x00\r\n") || strings.HasPrefix(source, "-") || strings.HasPrefix(ref, "-") || strings.Contains(relative, "\\") || path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return skillcmd.Origin{}, skillcmd.ErrInvalid
	}
	kind := request.Kind
	parsed, err := url.Parse(source)
	if kind == "" {
		if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http" || parsed.Scheme == "ssh" || parsed.Scheme == sourceGit || parsed.Scheme == fileScheme) || scpSource(source) {
			kind = sourceGit
		} else {
			kind = sourceLocal
		}
	}
	origin := skillcmd.Origin{Kind: kind, Source: source, Ref: ref, Path: relative}
	switch kind {
	case sourceLocal:
		if ref != "" {
			return skillcmd.Origin{}, skillcmd.ErrInvalid
		}
		absolute, err := filepath.Abs(source)
		if err != nil {
			return skillcmd.Origin{}, skillcmd.ErrInvalid
		}
		origin.Source = absolute
	case sourceGit:
		if !scpSource(source) && (err != nil || parsed.Host == "" && parsed.Scheme != fileScheme || parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh" && parsed.Scheme != sourceGit && parsed.Scheme != fileScheme) {
			return skillcmd.Origin{}, skillcmd.ErrInvalid
		}
		if err == nil && parsed.Scheme == fileScheme && (!filepath.IsAbs(parsed.Path) || parsed.Host != "" && parsed.Host != "localhost") {
			return skillcmd.Origin{}, skillcmd.ErrInvalid
		}
	default:
		return skillcmd.Origin{}, skillcmd.ErrInvalid
	}
	return origin, nil
}

func scpSource(source string) bool {
	if strings.Contains(source, "://") {
		return false
	}
	host, relative, ok := strings.Cut(source, ":")
	return ok && len(host) > 1 && relative != "" && !strings.HasPrefix(relative, ":") && !strings.ContainsAny(host, "/\\ \t")
}

func originLabel(origin skillcmd.Origin) string {
	if origin.Kind == sourceLocal {
		return "Local skill source"
	}
	if scpSource(origin.Source) {
		return gitSourceLabel
	}
	parsed, err := url.Parse(origin.Source)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme == fileScheme {
			return gitSourceLabel
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.RawFragment = ""
		return "Git: " + parsed.String()
	}
	// SCP syntax includes the SSH username. Do not project the private input.
	return gitSourceLabel
}

func (s *Service) capture(ctx context.Context, origin skillcmd.Origin, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, string, error) {
	if err := ctx.Err(); err != nil {
		return runtimecatalogcmd.Source{}, "", err
	}
	source := origin.Source
	commit := ""
	if origin.Kind == sourceGit {
		staging := filepath.Join(s.stagingDir, "source-"+uuid.NewString())
		if err := os.Mkdir(staging, 0700); err != nil {
			return runtimecatalogcmd.Source{}, "", skillcmd.ErrUnavailable
		}
		defer func() { _ = os.RemoveAll(staging) }()
		timeout, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		source = filepath.Join(staging, "checkout")
		if _, err := gitOutput(timeout, "", "clone", "--quiet", "--no-checkout", "--", origin.Source, source); err != nil {
			return runtimecatalogcmd.Source{}, "", err
		}
		ref := origin.Ref
		if ref == "" {
			ref = "HEAD"
		}
		resolved, err := gitOutput(timeout, source, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err != nil && timeout.Err() == nil {
			// Clone creates only the default local branch. Resolve other branch
			// names through their remote tracking refs without changing origin.
			remote := "refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")
			resolved, err = gitOutput(timeout, source, "rev-parse", "--verify", "--end-of-options", remote+"^{commit}")
		}
		if err != nil {
			return runtimecatalogcmd.Source{}, "", err
		}
		commit = strings.TrimSpace(resolved)
		if len(commit) != 40 && len(commit) != 64 {
			return runtimecatalogcmd.Source{}, "", skillcmd.ErrUnavailable
		}
		if _, err := gitOutput(timeout, source, "checkout", "--quiet", "--detach", commit, "--"); err != nil {
			return runtimecatalogcmd.Source{}, "", err
		}
		// This is our disposable clone, never a supplied source directory.
		// Git's private origin/config/reflogs are not skill payload resources.
		if err := os.RemoveAll(filepath.Join(source, ".git")); err != nil {
			return runtimecatalogcmd.Source{}, "", skillcmd.ErrUnavailable
		}
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return runtimecatalogcmd.Source{}, "", skillcmd.ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	captured, err := s.catalog.CaptureCandidate(ctx, root, origin.Path, id)
	return captured, commit, safeError(err)
}

// Git errors must not carry argv, stderr or credential-bearing source URLs.
func gitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, sourceGit, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	command.WaitDelay = time.Second
	var output gitBuffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", skillcmd.ErrUnavailable
	}
	return output.String(), nil
}

type gitBuffer struct{ bytes.Buffer }

func (b *gitBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, skillcmd.ErrUnavailable
	}
	return b.Buffer.Write(p)
}
