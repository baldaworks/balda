// Package backoffice owns Backoffice configuration, lifecycle, and use cases.
package backoffice

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

const (
	defaultListenAddr      = "127.0.0.1:8095"
	defaultPublicURL       = "http://127.0.0.1:8095"
	defaultAccessTokenTTL  = 15 * time.Minute
	defaultRefreshTokenTTL = 12 * time.Hour
	minimumAccessTokenTTL  = time.Minute
	maximumAccessTokenTTL  = time.Hour
	maximumRefreshTokenTTL = 30 * 24 * time.Hour
	httpsScheme            = "https"
)

// ServerConfig is the serialized Backoffice listener and browser-session configuration.
type ServerConfig struct {
	ListenAddr      string `mapstructure:"listen_addr"`
	PublicURL       string `mapstructure:"public_url"`
	AccessTokenTTL  string `mapstructure:"access_token_ttl"`
	RefreshTokenTTL string `mapstructure:"refresh_token_ttl"`
	QAUI            bool   `mapstructure:"qa_ui"`
}

// ResolvedServerConfig contains validated runtime values.
type ResolvedServerConfig struct {
	ListenAddr      string
	PublicURL       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	SecureCookies   bool
	QAUI            bool
}

// Resolve validates listener exposure, public URL, and bounded token lifetimes.
func (c ServerConfig) Resolve() (ResolvedServerConfig, error) {
	listenAddr := strings.TrimSpace(c.ListenAddr)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil || strings.TrimSpace(port) == "" {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.listen_addr must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.listen_addr port must be between 1 and 65535")
	}
	publicURL := strings.TrimSpace(c.PublicURL)
	if publicURL == "" {
		publicURL = defaultPublicURL
	}
	parsedURL, err := url.Parse(publicURL)
	if err != nil || parsedURL.Host == "" || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || parsedURL.User != nil || (parsedURL.Path != "" && parsedURL.Path != "/") {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.public_url must be an origin without credentials, query, fragment, or path")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != httpsScheme {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.public_url must use http or https")
	}
	if !isLoopbackHost(host) && parsedURL.Scheme != httpsScheme {
		return ResolvedServerConfig{}, fmt.Errorf("non-loopback Backoffice listeners require an HTTPS public_url")
	}
	accessTTL, err := resolveDuration(c.AccessTokenTTL, defaultAccessTokenTTL, "access_token_ttl")
	if err != nil {
		return ResolvedServerConfig{}, err
	}
	refreshTTL, err := resolveDuration(c.RefreshTokenTTL, defaultRefreshTokenTTL, "refresh_token_ttl")
	if err != nil {
		return ResolvedServerConfig{}, err
	}
	if accessTTL < minimumAccessTokenTTL || accessTTL > maximumAccessTokenTTL {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.access_token_ttl must be between %s and %s", minimumAccessTokenTTL, maximumAccessTokenTTL)
	}
	if refreshTTL <= accessTTL || refreshTTL > maximumRefreshTokenTTL {
		return ResolvedServerConfig{}, fmt.Errorf("balda.backoffice.refresh_token_ttl must exceed access_token_ttl and be at most %s", maximumRefreshTokenTTL)
	}
	return ResolvedServerConfig{
		ListenAddr: listenAddr, PublicURL: strings.TrimSuffix(publicURL, "/"),
		AccessTokenTTL: accessTTL, RefreshTokenTTL: refreshTTL,
		SecureCookies: parsedURL.Scheme == httpsScheme, QAUI: c.QAUI,
	}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func resolveDuration(raw string, fallback time.Duration, field string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("balda.backoffice.%s must be a positive duration", field)
	}
	return value, nil
}

// BaldaConfig is the allowlisted configuration subset consumed by Backoffice.
type BaldaConfig struct {
	Telegram TelegramConfig `mapstructure:"telegram"`
	Zulip    ZulipConfig    `mapstructure:"zulip"`
	Slack    SlackConfig    `mapstructure:"slack"`
	Webhooks WebhooksConfig `mapstructure:"webhooks"`
}

// TelegramConfig contains only non-secret capability information.
type TelegramConfig struct {
	Enabled bool                  `mapstructure:"enabled"`
	Webhook TelegramWebhookConfig `mapstructure:"webhook"`
}

// TelegramWebhookConfig is the safe enablement/address subset.
type TelegramWebhookConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	Path       string `mapstructure:"path"`
}

// ZulipConfig contains only non-secret capability information.
type ZulipConfig struct {
	Webhook ZulipWebhookConfig `mapstructure:"webhook"`
}

// ZulipWebhookConfig is the safe enablement/address subset.
type ZulipWebhookConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	Path       string `mapstructure:"path"`
}

// SlackConfig contains only chat and agent capability information.
type SlackConfig struct {
	Enabled    bool             `mapstructure:"enabled"`
	ListenAddr string           `mapstructure:"listen_addr"`
	EventsPath string           `mapstructure:"events_path"`
	Agent      SlackAgentConfig `mapstructure:"agent"`
}

// SlackAgentConfig is the safe enablement/address subset.
type SlackAgentConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	ListenAddr      string `mapstructure:"listen_addr"`
	EventsPath      string `mapstructure:"events_path"`
	EnableStreaming bool   `mapstructure:"enable_streaming"`
}

// WebhooksConfig contains only enablement and a safe route count.
type WebhooksConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	RouteCount int    `mapstructure:"route_count"`
}

// ResolvedConfig contains the selected database and safe Backoffice runtime settings.
type ResolvedConfig struct {
	Balda    BaldaConfig
	Server   ResolvedServerConfig
	Database state.DatabaseConfig
}
