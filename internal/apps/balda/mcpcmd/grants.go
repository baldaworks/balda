package mcpcmd

import "time"

const (
	ClientAuthNone        = "none"
	ClientAuthSecretBasic = "client_secret_basic"
	ClientAuthSecretPost  = "client_secret_post"
)

// AuthBinding identifies one installation-owned authorization context.
// A grant never transfers to a different connection, resource, issuer or client.
type AuthBinding struct {
	ConnectionID string `json:"connection_id"`
	Resource     string `json:"resource"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
}

type GrantStatus string

const (
	GrantAuthRequired GrantStatus = "auth_required"
	GrantAuthorized   GrantStatus = "authorized"
	GrantDisconnected GrantStatus = "disconnected"
)

// Grant holds public registration/status and opaque encrypted credentials.
// Its generation fences authorization completion, renewal and disconnect.
type Grant struct {
	ID                      string      `json:"id"`
	Binding                 AuthBinding `json:"binding"`
	Generation              uint64      `json:"generation"`
	Status                  GrantStatus `json:"status"`
	Scopes                  []string    `json:"scopes,omitempty"`
	TokenEndpointAuthMethod string      `json:"token_endpoint_auth_method"`
	ClientSecretExpiresAt   time.Time   `json:"client_secret_expires_at,omitempty"`
	AccessExpiresAt         time.Time   `json:"access_expires_at,omitempty"`
	ProtectedValues         []byte      `json:"-"`
	CreatedAt               time.Time   `json:"created_at"`
	UpdatedAt               time.Time   `json:"updated_at"`
}

type GrantOperation string

const (
	GrantRegister   GrantOperation = "register"
	GrantAuthorize  GrantOperation = "authorize"
	GrantRenew      GrantOperation = "renew"
	GrantDisconnect GrantOperation = "disconnect"
)
