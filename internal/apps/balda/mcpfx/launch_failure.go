package mcpfx

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// launchObservation stays private to one discovery attempt. The bridge invokes
// it at its fixed upstream, so a local status/header cannot manufacture proof.
type launchObservation struct {
	mu     sync.Mutex
	origin *url.URL
	reason mcpruntime.FailureReason
	fatal  bool
}

func newLaunchObservation(endpoint string) *launchObservation {
	origin, _ := url.Parse(endpoint)
	return &launchObservation{origin: origin}
}

func (o *launchObservation) observe(status int, headers http.Header, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case err == nil && status >= 200 && status < 300:
		// Discovery may fall back to initialize after a challenge. A later
		// successful exchange cannot prove that its protocol failure is OAuth.
		o.reason = ""
	case errors.Is(err, mcpcmd.ErrAuthRequired), errors.Is(err, mcpcmd.ErrDisconnected):
		o.reason = mcpruntime.FailureAuthorizationRequired
	case err == nil && status == http.StatusUnauthorized && oauthChallenge(headers, o.origin):
		o.reason = mcpruntime.FailureAuthorizationChallenge
	default:
		o.fatal = true
	}
}

func (o *launchObservation) failure() mcpruntime.FailureReason {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fatal || o.reason == "" {
		return mcpruntime.FailureUnavailable
	}
	return o.reason
}

func oauthChallenge(headers http.Header, origin *url.URL) bool {
	values := headers.Values("WWW-Authenticate")
	if len(values) == 0 || len(values) > 4 || origin == nil {
		return false
	}
	bytes := 0
	for _, value := range values {
		bytes += len(value)
	}
	if bytes > 4096 {
		return false
	}
	challenges, err := oauthex.ParseWWWAuthenticate(values)
	if err != nil || len(challenges) != 1 || challenges[0].Scheme != "bearer" {
		return false
	}
	params := challenges[0].Params
	if code := params["error"]; code != "" && code != "invalid_token" {
		return false
	}
	metadata := params["resource_metadata"]
	if !validOAuthURL(metadata) {
		return false
	}
	location, err := url.Parse(metadata)
	return err == nil && sameHTTPOrigin(location, origin) && location.Path != "" && location.RawQuery == "" && location.RawPath == ""
}

func launchFailure(ctx context.Context, observe func() mcpruntime.FailureReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reason := mcpruntime.FailureUnavailable
	if observe != nil {
		reason = observe()
	}
	return &mcpruntime.LaunchError{Reason: reason}
}
