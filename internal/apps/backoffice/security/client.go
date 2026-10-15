package security

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type sessionClientKey struct{}

type sessionClient struct {
	deviceLabel    string
	connectionPeer string
}

// withSessionClient records only the direct connection peer. Forwarded headers
// are intentionally ignored because no trusted proxy chain is configured.
func withSessionClient(r *http.Request) context.Context {
	client := sessionClient{deviceLabel: deviceLabel(r.UserAgent())}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Zone() == "" {
			client.connectionPeer = address.Unmap().String()
		}
	}
	return context.WithValue(r.Context(), sessionClientKey{}, client)
}

func sessionClientFromContext(ctx context.Context) (string, string) {
	client, _ := ctx.Value(sessionClientKey{}).(sessionClient)
	return client.deviceLabel, client.connectionPeer
}

func deviceLabel(userAgent string) string {
	if userAgent == "" {
		return ""
	}
	var browser string
	switch {
	case strings.Contains(userAgent, "Edg/"):
		browser = "Edge"
	case strings.Contains(userAgent, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(userAgent, "Chrome/") || strings.Contains(userAgent, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(userAgent, "Safari/"):
		browser = "Safari"
	default:
		return ""
	}
	var platform string
	switch {
	case strings.Contains(userAgent, "Android"):
		platform = "Android"
	case strings.Contains(userAgent, "iPhone") || strings.Contains(userAgent, "iPad"):
		platform = "iOS"
	case strings.Contains(userAgent, "Windows"):
		platform = "Windows"
	case strings.Contains(userAgent, "Macintosh") || strings.Contains(userAgent, "Mac OS X"):
		platform = "macOS"
	case strings.Contains(userAgent, "Linux"):
		platform = "Linux"
	default:
		return browser
	}
	return browser + " on " + platform
}
