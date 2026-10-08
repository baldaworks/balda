package webhook

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/destinationcmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
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

// RouteEnvelopeConfig configures optional final report delivery.
type RouteEnvelopeConfig struct {
	ReportTo      *RouteTargetConfig
	AckOnDelivery bool
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
	ReportTo       *destinationcmd.Target
	AckOnDelivery  bool
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
		var reportTo *destinationcmd.Target
		if rawRoute.Envelope.ReportTo != nil {
			reportTo = &destinationcmd.Target{
				Target: strings.TrimSpace(rawRoute.Envelope.ReportTo.Target),
				Key:    strings.TrimSpace(rawRoute.Envelope.ReportTo.Key),
			}
			if reportTo.Key == "" {
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.report_to.key is required", routeName)
			}
			switch reportTo.Target {
			case destinationcmd.TargetAlias, destinationcmd.TargetManagedAlias:
			case destinationcmd.TargetLocator:
				if _, err := locatorref.Parse(reportTo.Key); err != nil {
					return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.report_to.key: invalid public locator", routeName)
				}
			default:
				return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.report_to.target must be alias, managed_alias, or locator", routeName)
			}
		}
		if rawRoute.Envelope.AckOnDelivery && reportTo == nil {
			return normalizedConfig{}, fmt.Errorf("balda.webhooks.routes.%s.envelope.ack_on_delivery requires report_to", routeName)
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
			ReportTo:       reportTo,
			AckOnDelivery:  rawRoute.Envelope.AckOnDelivery,
			Auth:           authPol,
			Dedupe:         dedupePol,
		}
	}

	return normalized, nil
}
