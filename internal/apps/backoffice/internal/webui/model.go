// Package webui owns Backoffice-safe view models, embedded presentation, and
// the SSR/HTMX response protocol.
package webui

import (
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// Location is an allowlisted Backoffice browser location.
type Location string

const (
	LocationLogin    Location = "/login"
	LocationOverview Location = "/overview"
	LocationAccess   Location = "/access"
	LocationAccount  Location = "/account"
	LocationAudit    Location = "/audit"
)

// Valid reports whether a location can be emitted by server navigation.
func (l Location) Valid() bool {
	switch l {
	case LocationLogin, LocationOverview, LocationAccess, LocationAccount, LocationAudit:
		return true
	default:
		return false
	}
}

// NavItem is one server-authorized navigation item.
type NavItem struct {
	Label   string
	Href    Location
	Icon    string
	Current bool
}

// CapabilityCard is a configured-only, secret-free integration projection.
type CapabilityCard struct {
	ID         string
	Name       string
	Mode       string
	ListenAddr string
	Endpoint   string
	RouteCount int
	Streaming  bool
}

// BindingView is the optional single transport binding shown read-only.
type BindingView struct {
	ChannelType string
	Principal   string
	DisplayName string
	Provenance  string
}

// UserView is the canonical user's safe browser projection.
type UserView struct {
	ID                string
	DisplayName       string
	Username          string
	Status            string
	Role              string
	CredentialState   string
	MustChange        bool
	Primary           bool
	Binding           *BindingView
	Version           uint64
	CredentialVersion uint64
}

// SessionView represents one server-side family, never an individual token generation.
type SessionView struct {
	ID         string
	Assurance  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
	Revoked    bool
	Current    bool
	Version    uint64
}

// AuditView is a safe immutable security-event projection.
type AuditView struct {
	ID             string
	Action         string
	Outcome        string
	ActorUserID    string
	ActorSessionID string
	TargetType     string
	TargetID       string
	OccurredAt     time.Time
}

// ErrorView is a user-facing error without provider internals.
type ErrorView struct {
	Heading string
	Message string
}

// Page is the closed safe model accepted by production templates.
type Page struct {
	Title           string
	Current         Location
	Navigation      []NavItem
	Capabilities    []CapabilityCard
	Users           []UserView
	User            *UserView
	Sessions        []SessionView
	Audit           []AuditView
	Error           *ErrorView
	CSRFToken       string
	ReturnTo        string
	AuditAction     string
	AuditOutcome    string
	AuditTargetType string
	NextURL         string
}

// Navigation derives visible workspaces only from current server capabilities.
func Navigation(capabilities usercmd.BackofficeCapabilities, current Location) []NavItem {
	items := make([]NavItem, 0, 4)
	add := func(enabled bool, label string, href Location, icon string) {
		if enabled {
			items = append(items, NavItem{Label: label, Href: href, Icon: icon, Current: current == href})
		}
	}
	add(capabilities.Overview, "Overview", LocationOverview, "bi-speedometer2")
	add(capabilities.ManageUsers, "Access", LocationAccess, "bi-people")
	add(capabilities.Account, "Account", LocationAccount, "bi-person-circle")
	add(capabilities.ViewAudit, "Audit", LocationAudit, "bi-shield-check")
	return items
}

// ProjectUser constructs the explicit safe projection of a canonical user.
func ProjectUser(user usercmd.User) UserView {
	view := UserView{
		ID: user.ID, DisplayName: user.DisplayName, Username: user.Username,
		Status: string(user.Status), Role: string(user.Role), CredentialState: string(user.Credential.State),
		MustChange: user.Credential.MustChange, Primary: user.Primary,
		Version: user.Version, CredentialVersion: user.Credential.Version,
	}
	if user.Binding != nil {
		view.Binding = &BindingView{
			ChannelType: user.Binding.ChannelType, Principal: user.Binding.Principal,
			DisplayName: user.Binding.DisplayName, Provenance: user.Binding.Provenance,
		}
	}
	return view
}

// ProjectSession constructs one family-level session row.
func ProjectSession(summary usercmd.SessionSummary, currentFamilyID string) SessionView {
	return SessionView{
		ID: summary.ID, Assurance: string(summary.Assurance), CreatedAt: summary.CreatedAt,
		LastSeenAt: summary.LastSeenAt, ExpiresAt: summary.ExpiresAt, RevokedAt: summary.RevokedAt,
		Revoked: !summary.RevokedAt.IsZero(), Current: summary.ID == currentFamilyID, Version: summary.Version,
	}
}

// ProjectAudit constructs a safe audit row.
func ProjectAudit(event usercmd.AuditEvent) AuditView {
	return AuditView{
		ID: safeAuditID(event.ID), Action: string(event.Action), Outcome: string(event.Outcome),
		ActorUserID: safeAuditID(event.ActorUserID), ActorSessionID: safeAuditID(event.ActorSessionID),
		TargetType: string(event.TargetType), TargetID: safeAuditID(event.TargetID), OccurredAt: event.OccurredAt,
	}
}

func safeAuditID(value string) string {
	if value == "" {
		return ""
	}
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "[redacted]"
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !isHexCharacter(character) {
			return "[redacted]"
		}
	}
	return value
}

func isHexCharacter(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}

func (p Page) validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("page title is required")
	}
	if p.Current != "" && !p.Current.Valid() {
		return fmt.Errorf("page location is invalid")
	}
	return nil
}
