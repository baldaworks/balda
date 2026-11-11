// Package managementclient adapts the generated management API client to the
// standalone Backoffice edge.
package managementclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const (
	requestTimeout   = 5 * time.Second
	maxResponseBytes = 4 << 20
	maxTokenBytes    = 4096
)

var (
	// ErrUnavailable means Core could not provide a trustworthy response.
	ErrUnavailable = errors.New("core management API is unavailable")
	// ErrUnauthorized means the operator credential was rejected.
	ErrUnauthorized = errors.New("core management operator authentication failed")
	// ErrIncompatible means Core exposes a different management contract.
	ErrIncompatible = errors.New("core management API is incompatible")
	// ErrInternal means Core returned an unexpected response.
	ErrInternal = errors.New("core management API returned an invalid response")
)

// Config contains the server-only generated client inputs.
type Config struct {
	BaseURL       string
	SocketPath    string
	OperatorToken string
	HTTPClient    *http.Client
}

// Client is the explicit standalone Backoffice adapter for management v1.
type Client struct {
	generated *managementv1.ClientWithResponses
}

// New constructs a bounded generated management client.
func New(config Config) (*Client, error) {
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil || baseURL.Scheme != "http" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" ||
		baseURL.Fragment != "" || (baseURL.Path != "" && baseURL.Path != "/") {
		return nil, fmt.Errorf("core management URL must be an absolute loopback HTTP URL")
	}
	host := net.ParseIP(baseURL.Hostname())
	if host == nil || !host.IsLoopback() {
		return nil, fmt.Errorf("core management URL must be an absolute loopback HTTP URL")
	}
	if !validBearerToken(config.OperatorToken) {
		return nil, fmt.Errorf("core management operator token is invalid")
	}
	if config.SocketPath != "" && (!filepath.IsAbs(config.SocketPath) || filepath.Clean(config.SocketPath) != config.SocketPath || strings.ContainsAny(config.SocketPath, "\x00\r\n")) {
		return nil, fmt.Errorf("core management socket path must be a canonical absolute path")
	}
	httpClient := http.Client{}
	if config.HTTPClient != nil {
		httpClient = *config.HTTPClient
	}
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = requestTimeout
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	baseTransport := httpClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	if config.SocketPath != "" {
		dialer := &net.Dialer{Timeout: requestTimeout}
		baseTransport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", config.SocketPath)
			},
			IdleConnTimeout: requestTimeout,
		}
	}
	httpClient.Transport = boundedTransport{base: baseTransport}
	generated, err := managementv1.NewClientWithResponses(config.BaseURL,
		managementv1.WithHTTPClient(&httpClient),
		managementv1.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+config.OperatorToken)
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("construct generated Core management client: %w", err)
	}
	return &Client{generated: generated}, nil
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > maxTokenBytes {
		return false
	}
	content, padding := false, false
	for _, char := range token {
		if char == '=' {
			if !content {
				return false
			}
			padding = true
			continue
		}
		if padding || !isBearerCharacter(char) {
			return false
		}
		content = true
	}
	return content
}

func isBearerCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
		strings.ContainsRune("-._~+/", char)
}

// Compatibility verifies the exact management contract required by this BFF.
func (c *Client) Compatibility(ctx context.Context) error {
	if c == nil || c.generated == nil {
		return ErrUnavailable
	}
	response, err := c.generated.GetManagementCompatibilityWithResponse(ctx)
	if err != nil {
		return ErrUnavailable
	}
	if response == nil {
		return ErrUnavailable
	}
	switch response.StatusCode() {
	case http.StatusOK:
		if response.JSON200 == nil {
			return ErrInternal
		}
		if response.JSON200.ContractID != managementv1.BaldaManagementV1 ||
			response.JSON200.ContractVersion != managementv1.ContractVersion || response.JSON200.APIMajor != managementv1.N1 {
			return ErrIncompatible
		}
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrUnavailable
	default:
		return ErrInternal
	}
}

type boundedTransport struct {
	base http.RoundTripper
}

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = limitedReadCloser{Reader: io.LimitReader(response.Body, maxResponseBytes+1), Closer: response.Body}
	return response, nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}
