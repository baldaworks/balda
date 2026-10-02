package mcpcmd

import "time"

// BrowserAuthorization exposes only native redirect instructions and safe ID.
type BrowserAuthorization struct {
	ID               string
	ConnectionID     string
	AuthorizationURL string `json:"-"`
	ExpiresAt        time.Time
}

// BrowserCallback is write-only protocol input bound to current browser authority.
type BrowserCallback struct {
	State     string `json:"-"`
	Code      string `json:"-"`
	Issuer    string
	Denied    bool
	Authority Authority
}
