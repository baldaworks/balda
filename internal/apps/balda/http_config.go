package balda

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice"
)

// HTTPConfig controls Balda's shared HTTP bind and public URL prefix.
type HTTPConfig struct {
	ListenAddr string  `mapstructure:"listen_addr"`
	BaseURL    string  `mapstructure:"base_url"`
	BasePath   *string `mapstructure:"base_path"`
}

// ResolvedHTTPConfig holds validated shared HTTP settings.
type ResolvedHTTPConfig struct {
	ListenAddr string
	BaseURL    string
	BasePath   string
}

// ResolveHTTP resolves shared settings, falling back to the old Backoffice settings.
func (c BaldaConfig) ResolveHTTP() (ResolvedHTTPConfig, error) {
	defaults, err := (backoffice.ServerConfig{}).Resolve()
	if err != nil {
		return ResolvedHTTPConfig{}, fmt.Errorf("resolve default HTTP settings: %w", err)
	}

	listenAddr := strings.TrimSpace(c.HTTP.ListenAddr)
	if listenAddr == "" {
		listenAddr = strings.TrimSpace(c.Backoffice.ListenAddr)
	}
	if listenAddr == "" {
		listenAddr = defaults.ListenAddr
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.listen_addr must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.listen_addr port must be between 1 and 65535")
	}
	if strings.ContainsAny(host, "/?#% \t\r\n") {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.listen_addr must be a host:port address")
	}

	baseURL := strings.TrimSpace(c.HTTP.BaseURL)
	if baseURL == "" {
		legacy, err := (backoffice.ServerConfig{PublicURL: c.Backoffice.PublicURL}).Resolve()
		if err != nil {
			return ResolvedHTTPConfig{}, err
		}
		baseURL = legacy.PublicURL
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || baseURL != parsedURL.Scheme+"://"+parsedURL.Host {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.base_url must be an origin without credentials, query, fragment, or path")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.base_url must use http or https")
	}
	if !isHTTPBindLoopback(host) && parsedURL.Scheme != "https" {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.base_url must use HTTPS for a non-loopback balda.http.listen_addr")
	}

	basePath := c.Backoffice.BasePath
	if c.HTTP.BasePath != nil {
		basePath = *c.HTTP.BasePath
	}
	if basePath != "" && (basePath == "/" || !strings.HasPrefix(basePath, "/") || strings.HasSuffix(basePath, "/") || strings.ContainsAny(basePath, "\\?#% \t\r\n") || strings.Contains(basePath, "//") || path.Clean(basePath) != basePath) {
		return ResolvedHTTPConfig{}, fmt.Errorf("balda.http.base_path must be an empty or canonical absolute path without a trailing slash")
	}

	return ResolvedHTTPConfig{ListenAddr: listenAddr, BaseURL: baseURL, BasePath: basePath}, nil
}

// ResolveBackofficeServer preserves the standalone Backoffice settings until cutover.
func (c BaldaConfig) ResolveBackofficeServer() (backoffice.ResolvedServerConfig, error) {
	return c.Backoffice.Resolve()
}

// ResolveSharedBackofficeServer prepares the browser handler for the shared listener.
func (c BaldaConfig) ResolveSharedBackofficeServer() (backoffice.ResolvedServerConfig, error) {
	httpConfig, err := c.ResolveHTTP()
	if err != nil {
		return backoffice.ResolvedServerConfig{}, err
	}
	server := c.Backoffice
	server.ListenAddr = httpConfig.ListenAddr
	server.PublicURL = httpConfig.BaseURL
	server.BasePath = httpConfig.BrowserPath()
	return server.Resolve()
}

// BrowserPath returns the Backoffice mount path without a trailing slash.
func (c ResolvedHTTPConfig) BrowserPath() string {
	return c.BasePath + "/backoffice"
}

// ManagedWebhookPath returns the callback path for a validated route name.
func (c ResolvedHTTPConfig) ManagedWebhookPath(name string) string {
	return c.BasePath + "/webhooks/" + name
}

// GatewayPath returns the callback path for validated transport and endpoint segments.
func (c ResolvedHTTPConfig) GatewayPath(transport, endpoint string) string {
	return c.BasePath + "/gateway/" + transport + "/" + endpoint
}

// PublicURL attaches a root-relative route path to the public origin.
func (c ResolvedHTTPConfig) PublicURL(routePath string) string {
	return c.BaseURL + routePath
}

func isHTTPBindLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
