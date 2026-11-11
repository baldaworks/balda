package managementhttp

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const maxOperatorTokenBytes = 4096

// Config controls the dedicated management listener and its service credential.
type Config struct {
	Enabled       bool   `mapstructure:"enabled"`
	ListenAddr    string `mapstructure:"listen_addr"`
	SocketPath    string `mapstructure:"socket_path"`
	OperatorToken string `mapstructure:"operator_token" json:"-" yaml:"-"`
}

// ResolvedConfig contains validated management transport inputs.
type ResolvedConfig struct {
	Enabled    bool
	ListenAddr string
	SocketPath string
	auth       operatorAuthenticator
}

// Resolve validates deployment configuration without binding a socket.
func (c Config) Resolve() (ResolvedConfig, error) {
	return c.resolve(false)
}

func (c Config) resolve(allowPortZero bool) (ResolvedConfig, error) {
	if !c.Enabled {
		return ResolvedConfig{}, nil
	}

	var listenAddr string
	if c.SocketPath != "" {
		if !filepath.IsAbs(c.SocketPath) || filepath.Clean(c.SocketPath) != c.SocketPath || strings.ContainsAny(c.SocketPath, "\x00\r\n") {
			return ResolvedConfig{}, fmt.Errorf("balda.management.socket_path must be a canonical absolute path")
		}
	} else {
		var err error
		listenAddr, err = resolveListenAddr(c.ListenAddr, allowPortZero)
		if err != nil {
			return ResolvedConfig{}, err
		}
	}
	authenticator, err := newAuthenticator(c.OperatorToken)
	if err != nil {
		return ResolvedConfig{}, err
	}
	return ResolvedConfig{
		Enabled:    true,
		ListenAddr: listenAddr,
		SocketPath: c.SocketPath,
		auth:       authenticator,
	}, nil
}

func resolveListenAddr(raw string, allowPortZero bool) (string, error) {
	listenAddr := strings.TrimSpace(raw)
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("balda.management.listen_addr must be an explicit loopback IP host:port address")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !ip.IsLoopback() {
		return "", fmt.Errorf("balda.management.listen_addr must use an explicit loopback IP")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 || (!allowPortZero && portNumber == 0) {
		return "", fmt.Errorf("balda.management.listen_addr port must be between 1 and 65535")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(portNumber)), nil
}
