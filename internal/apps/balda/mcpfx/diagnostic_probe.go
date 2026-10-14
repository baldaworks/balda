package mcpfx

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const (
	diagnosticTimeout      = 10 * time.Second
	diagnosticCloseTimeout = time.Second
)

// diagnosticTokenReader returns an existing access token without refreshing
// or changing its grant. The grant owner implements this policy.
type diagnosticTokenReader interface {
	DiagnosticAccessToken(ctx context.Context, binding mcpcmd.AuthBinding, scopes []string) (string, error)
}

// diagnosticLauncher owns cleanup of a partially started SDK session before
// returning from cancellation or discovery failure.
type diagnosticLauncher interface {
	StartDiagnostic(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig, closeTimeout time.Duration) (mcpruntime.Instance, error)
}

// DiagnosticProbe performs one bounded, read-only MCP handshake and complete
// tool discovery without publishing the connection into a runtime catalog.
type DiagnosticProbe struct {
	values       managedValueResolver
	tokens       diagnosticTokenReader
	launcher     diagnosticLauncher
	timeout      time.Duration
	closeTimeout time.Duration
}

// NewDiagnosticProbe binds private value resolution, read-only OAuth access,
// and the SDK launcher used by ordinary MCP discovery.
func NewDiagnosticProbe(values managedValueResolver, tokens diagnosticTokenReader, launcher diagnosticLauncher) (*DiagnosticProbe, error) {
	if values == nil || tokens == nil || launcher == nil {
		return nil, mcpcmd.ErrInvalid
	}
	return &DiagnosticProbe{
		values:       values,
		tokens:       tokens,
		launcher:     launcher,
		timeout:      diagnosticTimeout,
		closeTimeout: diagnosticCloseTimeout,
	}, nil
}

// DiagnoseMCP checks one exact revision and returns no transport or credential
// details. A failed result never carries a partial tool count.
func (p *DiagnosticProbe) DiagnoseMCP(ctx context.Context, revision mcpcmd.Revision) mcpcmd.DiagnosticResult {
	if p == nil || p.values == nil || p.tokens == nil || p.launcher == nil || ctx.Err() != nil {
		return failedDiagnostic()
	}
	closeTimeout := p.effectiveCloseTimeout()
	if closeTimeout <= 0 || p.timeout <= closeTimeout {
		return failedDiagnostic()
	}
	operationCtx, cancel := context.WithTimeout(ctx, p.timeout-closeTimeout)
	defer cancel()
	return p.diagnose(operationCtx, revision, closeTimeout)
}

func (p *DiagnosticProbe) diagnose(ctx context.Context, revision mcpcmd.Revision, closeTimeout time.Duration) mcpcmd.DiagnosticResult {
	config, err := ResolveManagedLaunch(revision, p.values)
	if err != nil {
		return failedDiagnostic()
	}
	config.Headers = cloneMap(config.Headers)
	if !revision.Definition.OAuth {
		if revision.Definition.AuthBinding != nil {
			return failedDiagnostic()
		}
	} else {
		binding := revision.Definition.AuthBinding
		if binding == nil || binding.ConnectionID != revision.ConnectionID || binding.Resource != revision.Definition.URL || hasAuthorizationHeader(config.Headers) {
			return failedDiagnostic()
		}
		token, err := p.tokens.DiagnosticAccessToken(ctx, *binding, revision.Definition.Scopes)
		if err != nil || token == "" || strings.ContainsAny(token, "\x00\r\n") {
			return failedDiagnostic()
		}
		if config.Headers == nil {
			config.Headers = make(map[string]string)
		}
		config.Headers["Authorization"] = "Bearer " + token
	}
	key := mcpruntime.InstanceKey{
		Name:     revision.ConnectionID,
		Revision: runtimecatalogcmd.RevisionID(revision.ID),
	}
	instance, err := p.launcher.StartDiagnostic(ctx, key, config, closeTimeout)
	if err != nil || instance == nil {
		return failedDiagnostic()
	}
	count := len(instance.Tools())
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer closeCancel()
	if err := instance.Close(closeCtx); err != nil {
		return failedDiagnostic()
	}
	if ctx.Err() != nil {
		return failedDiagnostic()
	}
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: count}
}

func (p *DiagnosticProbe) effectiveCloseTimeout() time.Duration {
	if p.closeTimeout < p.timeout/2 {
		return p.closeTimeout
	}
	return p.timeout / 2
}

func hasAuthorizationHeader(headers map[string]string) bool {
	for name := range headers {
		if http.CanonicalHeaderKey(name) == "Authorization" {
			return true
		}
	}
	return false
}

func failedDiagnostic() mcpcmd.DiagnosticResult {
	return mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}
}
