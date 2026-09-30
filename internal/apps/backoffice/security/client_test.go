package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionClientUsesConnectionPeerWithoutForwardedHeaders(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "https://backoffice.example/login", nil)
	request.RemoteAddr = "192.0.2.42:32100"
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/130.0")
	request.Header.Set("X-Forwarded-For", "198.51.100.99")
	request.Header.Set("Forwarded", "for=203.0.113.5")
	device, peer := sessionClientFromContext(withSessionClient(request))
	if device != "Firefox on Linux" || peer != "192.0.2.42" {
		t.Fatalf("session client = %q, %q", device, peer)
	}

	request.RemoteAddr = "invalid"
	request.Header.Set("User-Agent", "secret@example.com")
	device, peer = sessionClientFromContext(withSessionClient(request))
	if device != "" || peer != "" {
		t.Fatalf("unknown session client = %q, %q", device, peer)
	}
}
