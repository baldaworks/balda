package webhook

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
)

const (
	DefaultListenAddr = "127.0.0.1:8090"

	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 10 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 60 * time.Second
	MaxBodyBytes      = 1 << 20
)

const (
	RouteModeJob     = webhookapp.ModeJob
	RouteModeSession = webhookapp.ModeSession
)

const (
	AuthTypeNone   = "none"
	AuthTypeHeader = "header"
)

const (
	DedupeSourceRequestID = "request_id"
	DedupeSourceHeader    = "header"
	DedupeSourceBodySHA   = "body_sha256"
)

// RouteConfig configures one inbound webhook route.
type RouteConfig struct {
	Path           string
	PromptTemplate string
	Envelope       RouteEnvelopeConfig
	Auth           RouteAuthConfig
	Dedupe         RouteDedupeConfig
}

// RouteEnvelopeConfig configures the destination envelope for a webhook route.
type RouteEnvelopeConfig struct {
	Target   string
	Key      string
	Mode     string
	ReportTo *RouteTargetConfig
}

// RouteTargetConfig configures a destination target reference.
type RouteTargetConfig struct {
	Target string
	Key    string
}

// RouteAuthConfig configures HTTP authentication for a webhook route.
type RouteAuthConfig struct {
	Type   string
	Header string
	Value  string
}

// RouteDedupeConfig configures the deduplication key source for a webhook route.
type RouteDedupeConfig struct {
	Source string
	Header string
}

// Config controls inbound webhook routing and dispatch behavior.
type Config struct {
	Enabled    bool
	ListenAddr string
	Routes     map[string]RouteConfig
}

type route struct {
	Name           string
	Path           string
	PromptTemplate *template.Template
	Target         envelopetarget.Target
	Mode           string
	ReportTo       *envelopetarget.Target
	Auth           authPolicy
	Dedupe         dedupePolicy
}

type authPolicy struct {
	Type   string
	Header string
	Value  string
}

type dedupePolicy struct {
	Source string
	Header string
}

type normalizedConfig struct {
	Enabled    bool
	ListenAddr string
	Routes     map[string]route
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}
	normalized := normalizedConfig{
		Enabled:    cfg.Enabled,
		ListenAddr: listenAddr,
		Routes:     make(map[string]route),
	}
	if !cfg.Enabled {
		return normalized, nil
	}

	if len(cfg.Routes) == 0 {
		return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes is required when webhooks are enabled")
	}

	seenPaths := make(map[string]string, len(cfg.Routes))
	for rawName, rawRoute := range cfg.Routes {
		routeName := strings.TrimSpace(rawName)
		if routeName == "" {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes key is required")
		}

		path := strings.TrimSpace(rawRoute.Path)
		if path == "" {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.path: path is required", routeName)
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if existingName, exists := seenPaths[path]; exists {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.path duplicates route %q", routeName, existingName)
		}
		seenPaths[path] = routeName

		templateText := strings.TrimSpace(rawRoute.PromptTemplate)
		if templateText == "" {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.prompt_template is required", routeName)
		}
		tmpl, err := template.New("inbound_webhook." + routeName).Option("missingkey=error").Parse(templateText)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("invalid balda.webhooks.routes.%s.prompt_template: %w", routeName, err)
		}
		target := envelopetarget.Target{
			Target: strings.TrimSpace(rawRoute.Envelope.Target),
			Key:    strings.TrimSpace(rawRoute.Envelope.Key),
		}
		if target.Target == "" && target.Key == "" {
			target = envelopetarget.Target{Target: envelopetarget.TargetAlias, Key: envelopetarget.AliasOwner}
		}
		if target.Target == "" {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.target is required", routeName)
		}
		if target.Key == "" {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.key is required", routeName)
		}
		var reportTo *envelopetarget.Target
		if rawRoute.Envelope.ReportTo != nil {
			reportTo = &envelopetarget.Target{
				Target: strings.TrimSpace(rawRoute.Envelope.ReportTo.Target),
				Key:    strings.TrimSpace(rawRoute.Envelope.ReportTo.Key),
			}
			if reportTo.Target == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.report_to.target is required", routeName)
			}
			if reportTo.Key == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.report_to.key is required", routeName)
			}
		}
		mode := strings.ToLower(strings.TrimSpace(rawRoute.Envelope.Mode))
		if mode == "" {
			mode = RouteModeJob
		}
		switch mode {
		case RouteModeJob, RouteModeSession:
		default:
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.mode: unsupported mode %q", routeName, rawRoute.Envelope.Mode)
		}
		authPol := authPolicy{
			Type:   strings.ToLower(strings.TrimSpace(rawRoute.Auth.Type)),
			Header: strings.TrimSpace(rawRoute.Auth.Header),
			Value:  strings.TrimSpace(rawRoute.Auth.Value),
		}
		if authPol.Type == "" {
			authPol.Type = AuthTypeNone
		}
		switch authPol.Type {
		case AuthTypeNone:
			authPol = authPolicy{Type: AuthTypeNone}
		case AuthTypeHeader:
			if authPol.Header == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.auth: header is required for type=%q", routeName, AuthTypeHeader)
			}
			if authPol.Value == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.auth: value is required for type=%q", routeName, AuthTypeHeader)
			}
		default:
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.auth: unsupported type %q", routeName, rawRoute.Auth.Type)
		}
		dedupePol := dedupePolicy{
			Source: strings.ToLower(strings.TrimSpace(rawRoute.Dedupe.Source)),
			Header: strings.TrimSpace(rawRoute.Dedupe.Header),
		}
		if dedupePol.Source == "" && dedupePol.Header != "" {
			dedupePol.Source = DedupeSourceHeader
		}
		if dedupePol.Source == "" {
			dedupePol.Source = DedupeSourceRequestID
		}
		switch dedupePol.Source {
		case DedupeSourceRequestID, DedupeSourceBodySHA:
		case DedupeSourceHeader:
			if dedupePol.Header == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.dedupe: header is required for source=%q", routeName, DedupeSourceHeader)
			}
		default:
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.dedupe: unsupported source %q", routeName, rawRoute.Dedupe.Source)
		}

		normalized.Routes[path] = route{
			Name:           routeName,
			Path:           path,
			PromptTemplate: tmpl,
			Target:         target,
			Mode:           mode,
			ReportTo:       reportTo,
			Auth:           authPol,
			Dedupe:         dedupePol,
		}
	}

	return normalized, nil
}
