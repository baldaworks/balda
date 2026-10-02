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

// DeviceAuthorization carries instructions for a native no-store page only.
type DeviceAuthorization struct {
	ID, ConnectionID        string
	VerificationURI         string `json:"-"`
	VerificationURIComplete string `json:"-"`
	UserCode                string `json:"-"`
	ExpiresAt               time.Time
	Status                  DeviceStatus
}

// DeviceStatus is the safe progress of a transient device authorization.
type DeviceStatus string

const (
	DevicePending    DeviceStatus = "pending"
	DeviceAuthorized DeviceStatus = "authorized"
	DeviceDenied     DeviceStatus = "denied"
	DeviceExpired    DeviceStatus = "expired"
	DeviceFailed     DeviceStatus = "failed"
)
