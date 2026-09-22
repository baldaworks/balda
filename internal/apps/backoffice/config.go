// Package backoffice owns Backoffice configuration, lifecycle, and use cases.
package backoffice

import (
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/paths"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/appconfig"
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

//go:embed defaults.yaml
var defaultConfigYAML []byte

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

// BaldaDocumentConfig combines the allowlisted Balda subset with Backoffice settings.
type BaldaDocumentConfig struct {
	BaldaConfig `mapstructure:",squash"`
	Backoffice  ServerConfig `mapstructure:"backoffice"`
}

// BaldaConfig is the allowlisted configuration subset consumed by Backoffice.
type BaldaConfig struct {
	Telegram   TelegramConfig       `mapstructure:"telegram"`
	Zulip      ZulipConfig          `mapstructure:"zulip"`
	Slack      SlackConfig          `mapstructure:"slack"`
	Webhooks   WebhooksConfig       `mapstructure:"webhooks"`
	Logger     LoggerConfig         `mapstructure:"logger"`
	WorkingDir string               `mapstructure:"working_dir"`
	StateDir   string               `mapstructure:"state_dir"`
	Database   state.DatabaseConfig `mapstructure:"database"`
}

// LoggerConfig controls Backoffice structured logging.
type LoggerConfig struct {
	Level  string `mapstructure:"level"`
	Pretty bool   `mapstructure:"pretty"`
}

// TelegramConfig contains only fields required for configured-capability projection.
type TelegramConfig struct {
	Token   string                `mapstructure:"token"`
	Webhook TelegramWebhookConfig `mapstructure:"webhook"`
}

// TelegramWebhookConfig is the safe enablement/address subset.
type TelegramWebhookConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	Path       string `mapstructure:"path"`
}

// ZulipConfig contains only fields required for configured-capability projection.
type ZulipConfig struct {
	APIKey       string             `mapstructure:"api_key"`
	WebhookToken string             `mapstructure:"webhook_token"`
	Webhook      ZulipWebhookConfig `mapstructure:"webhook"`
}

// ZulipWebhookConfig is the safe enablement/address subset.
type ZulipWebhookConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ListenAddr string `mapstructure:"listen_addr"`
	Path       string `mapstructure:"path"`
}

// SlackConfig contains chat and agent enablement plus secret-bearing load-only fields.
type SlackConfig struct {
	Enabled       bool             `mapstructure:"enabled"`
	BotToken      string           `mapstructure:"bot_token"`
	SigningSecret string           `mapstructure:"signing_secret"`
	ListenAddr    string           `mapstructure:"listen_addr"`
	EventsPath    string           `mapstructure:"events_path"`
	Agent         SlackAgentConfig `mapstructure:"agent"`
}

// SlackAgentConfig is the safe enablement/address subset.
type SlackAgentConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	ListenAddr      string `mapstructure:"listen_addr"`
	EventsPath      string `mapstructure:"events_path"`
	EnableStreaming bool   `mapstructure:"enable_streaming"`
}

// WebhooksConfig contains enablement and opaque routes used only for a safe count.
type WebhooksConfig struct {
	Enabled    bool           `mapstructure:"enabled"`
	ListenAddr string         `mapstructure:"listen_addr"`
	Routes     map[string]any `mapstructure:"routes"`
}

// ConfigDocument is the app-specific .config/balda/config.yaml projection.
type ConfigDocument struct {
	Balda BaldaDocumentConfig `mapstructure:"balda"`
}

// LoadOptions selects the normal Balda config root and optional profile.
type LoadOptions struct {
	WorkingDir string
	ConfigDir  string
	Profile    string
}

// ResolvedConfig contains the selected database and safe Backoffice runtime settings.
type ResolvedConfig struct {
	Balda      BaldaConfig
	Server     ResolvedServerConfig
	WorkingDir string
	StateDir   string
	Database   state.DatabaseConfig
}

// LoadConfig loads .config/balda/config.yaml with BALDA_* overrides and resolves paths once.
func LoadConfig(options LoadOptions) (ResolvedConfig, error) {
	var document ConfigDocument
	settings, _, err := appconfig.LoadResolvedSettings(
		appconfig.RuntimeLoadOptions{WorkingDir: options.WorkingDir, ConfigDir: options.ConfigDir, Profile: options.Profile},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultConfigYAML, UseDotConfigAppDir: true},
	)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if err := appconfig.DecodeSettings(settings, &document); err != nil {
		return ResolvedConfig{}, fmt.Errorf("decode Backoffice config: %w", err)
	}
	configuredWorkingDir := strings.TrimSpace(document.Balda.WorkingDir)
	if configuredWorkingDir == "" {
		configuredWorkingDir = strings.TrimSpace(options.WorkingDir)
	} else if !filepath.IsAbs(configuredWorkingDir) && strings.TrimSpace(options.WorkingDir) != "" {
		configuredWorkingDir = filepath.Join(options.WorkingDir, configuredWorkingDir)
	}
	workingDir, err := paths.ResolveWorkingDir(configuredWorkingDir)
	if err != nil {
		return ResolvedConfig{}, err
	}
	stateDir, err := paths.ResolveStateDir(workingDir, document.Balda.StateDir)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("resolve balda state_dir: %w", err)
	}
	database, err := document.Balda.Database.Resolve(workingDir, stateDir)
	if err != nil {
		return ResolvedConfig{}, err
	}
	server, err := document.Balda.Backoffice.Resolve()
	if err != nil {
		return ResolvedConfig{}, err
	}
	return ResolvedConfig{
		Balda: document.Balda.BaldaConfig, Server: server,
		WorkingDir: workingDir, StateDir: stateDir, Database: database,
	}, nil
}
