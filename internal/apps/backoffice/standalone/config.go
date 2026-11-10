// Package standalone owns the narrow configuration and lifecycle boundary for
// the separate Backoffice process.
package standalone

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/baldaworks/runtime/v2/appconfig"
)

const (
	defaultListenAddr = "127.0.0.1:8095"
	defaultPublicURL  = "http://127.0.0.1:8095"
	maxTokenBytes     = 4096
)

// Document is the complete configuration projection used by cmd/backoffice.
// Unknown Balda and runtime settings are deliberately not represented.
type Document struct {
	Balda Config `mapstructure:"balda"`
}

// Config is the allowlisted Backoffice configuration projection.
type Config struct {
	HTTP       HTTPConfig       `mapstructure:"http"`
	Backoffice BackofficeConfig `mapstructure:"backoffice"`
	Management ManagementConfig `mapstructure:"management"`
}

// HTTPConfig identifies the canonical public browser origin and shared mount.
type HTTPConfig struct {
	BaseURL  string  `mapstructure:"base_url"`
	BasePath *string `mapstructure:"base_path"`
}

// BackofficeConfig controls only the standalone public listener.
type BackofficeConfig struct {
	ListenAddr string `mapstructure:"listen_addr"`
}

// ManagementConfig identifies the loopback Core API and its application token.
type ManagementConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	ListenAddr    string `mapstructure:"listen_addr"`
	OperatorToken string `mapstructure:"operator_token" json:"-" yaml:"-"`
}

// ResolvedConfig contains validated process inputs.
type ResolvedConfig struct {
	ListenAddr       string
	PublicOrigin     string
	MountPath        string
	CoreURL          string
	OperatorToken    string `json:"-" yaml:"-"`
	WebhookURLPrefix string
}

// LoadConfigDocument applies the standard Balda defaults/config/profile/env
// precedence to the narrow standalone document.
func LoadConfigDocument(options appconfig.RuntimeLoadOptions, defaults []byte) (Document, string, error) {
	var document Document
	profile, err := appconfig.LoadConfigDocument(options, appconfig.AppLoadOptions{
		AppName: "balda", DefaultsYAML: defaults, UseDotConfigAppDir: true,
	}, &document)
	if err != nil {
		return Document{}, "", err
	}
	return document, profile, nil
}

// Resolve validates the browser listener and Core client configuration without
// binding a socket or performing network I/O.
func (c Config) Resolve() (ResolvedConfig, error) {
	listenAddr, loopback, err := resolveListenAddr(c.Backoffice.ListenAddr, false)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("balda.backoffice.listen_addr: %w", err)
	}
	publicOrigin, secure, err := resolvePublicOrigin(c.HTTP.BaseURL)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if !loopback && !secure {
		return ResolvedConfig{}, fmt.Errorf("non-loopback Backoffice listeners require an HTTPS balda.http.base_url")
	}
	basePath := ""
	if c.HTTP.BasePath != nil {
		basePath = *c.HTTP.BasePath
	}
	if err := validateBasePath(basePath); err != nil {
		return ResolvedConfig{}, err
	}
	if !c.Management.Enabled {
		return ResolvedConfig{}, fmt.Errorf("balda.management.enabled must be true for standalone Backoffice")
	}
	managementAddr, managementLoopback, err := resolveListenAddr(c.Management.ListenAddr, true)
	if err != nil || !managementLoopback {
		return ResolvedConfig{}, fmt.Errorf("balda.management.listen_addr must be an explicit loopback IP host:port address")
	}
	if err := validateBearerToken(c.Management.OperatorToken); err != nil {
		return ResolvedConfig{}, err
	}
	return ResolvedConfig{
		ListenAddr: listenAddr, PublicOrigin: publicOrigin, MountPath: basePath + "/backoffice",
		CoreURL: "http://" + managementAddr, OperatorToken: c.Management.OperatorToken,
		WebhookURLPrefix: publicOrigin + basePath + "/webhooks/",
	}, nil
}

func resolveListenAddr(raw string, requireExplicitIP bool) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" && !requireExplicitIP {
		value = defaultListenAddr
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", false, fmt.Errorf("must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", false, fmt.Errorf("port must be between 1 and 65535")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if requireExplicitIP && (ip == nil || ip.IsUnspecified()) {
		return "", false, fmt.Errorf("must use an explicit IP address")
	}
	loopback := strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
	if requireExplicitIP && !loopback {
		return "", false, fmt.Errorf("must use a loopback IP address")
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), loopback, nil
}

func resolvePublicOrigin(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultPublicURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, fmt.Errorf("balda.http.base_url must be an HTTP origin without credentials, path, query, or fragment")
	}
	return strings.TrimSuffix(value, "/"), parsed.Scheme == "https", nil
}

func validateBasePath(value string) error {
	if value == "" {
		return nil
	}
	if value == "/" || !strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.ContainsAny(value, "\\?#% \t\r\n") || strings.Contains(value, "//") || path.Clean(value) != value {
		return fmt.Errorf("balda.http.base_path must be an empty or canonical absolute path without a trailing slash")
	}
	return nil
}

func validateBearerToken(token string) error {
	if token == "" {
		return fmt.Errorf("balda.management.operator_token is required")
	}
	if len(token) > maxTokenBytes {
		return fmt.Errorf("balda.management.operator_token must not exceed %d bytes", maxTokenBytes)
	}
	content, padding := false, false
	for _, char := range token {
		if char == '=' {
			if !content {
				return fmt.Errorf("balda.management.operator_token must be a valid bearer token")
			}
			padding = true
			continue
		}
		if padding || !isBearerCharacter(char) {
			return fmt.Errorf("balda.management.operator_token must be a valid bearer token")
		}
		content = true
	}
	if !content {
		return fmt.Errorf("balda.management.operator_token must be a valid bearer token")
	}
	return nil
}

func isBearerCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
		strings.ContainsRune("-._~+/", char)
}
