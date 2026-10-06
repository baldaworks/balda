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
	LocationMCP      Location = "/mcp"
)

// Valid reports whether a location can be emitted by server navigation.
func (l Location) Valid() bool {
	switch l {
	case LocationLogin, LocationOverview, LocationAccess, LocationAccount, LocationAudit, LocationMCP:
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
	ID                string
	ChannelType       string
	Principal         string
	DisplayName       string
	ProviderUsername  string
	ProviderFirstName string
	Provenance        string
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
	Bindings          []BindingView
	Version           uint64
	CredentialVersion uint64
}

// SessionView represents one server-side family, never an individual token generation.
type SessionView struct {
	ID             string
	Assurance      string
	CreatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time
	RevokedAt      time.Time
	Revoked        bool
	Expired        bool
	Current        bool
	DeviceLabel    string
	ConnectionPeer string
	Version        uint64
}

// AuditView is a safe immutable security-event projection.
type AuditView struct {
	ID             string
	Action         string
	ActionLabel    string
	Outcome        string
	ActorUserID    string
	ActorName      string
	ActorSessionID string
	TargetType     string
	TargetID       string
	TargetName     string
	OccurredAt     time.Time
}

// ErrorView is a user-facing error without provider internals.
type ErrorView struct {
	Heading string
	Message string
}

// QALink identifies one synthetic preview in the QA gallery.
type QALink struct {
	Label string
	Path  string
}

// MFAView contains only safe factor status and dates.
type MFAView struct {
	Enabled    bool
	Available  bool
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// MFACeremonyView holds public browser options and a transient server transaction.
type MFACeremonyView struct {
	Registration bool
	OptionsJSON  string
	Transaction  string
	FinishPath   string
	CancelPath   string
	Confirm      bool
}

// Page is the closed safe model accepted by production templates.
type Page struct {
	MCP          *MCPView
	RestartURL   string
	RestartLabel string
	MFA          *MFAView
	Ceremony     *MFACeremonyView

	StepUpURL string
	// ViewerUsername identifies the signed-in operator, independently of User.
	ViewerUsername      string
	Title               string
	Current             Location
	Navigation          []NavItem
	Capabilities        []CapabilityCard
	BindingForms        []BindingForm
	OwnUser             bool
	CreateUser          bool
	AccessSearch        string
	AccessRole          string
	AccessStatus        string
	Users               []UserView
	User                *UserView
	Sessions            []SessionView
	SessionActionPrefix string
	SessionNextURL      string
	MixedSessions       bool
	Audit               []AuditView
	Error               *ErrorView
	CSRFToken           string
	AutoRefresh         bool
	RefreshRetryable    bool
	ReturnTo            string
	AuditAction         string
	AuditOutcome        string
	AuditTargetType     string
	AuditActor          string
	AuditFrom           string
	AuditTo             string
	NextURL             string
	Gallery             []QALink
	Preview             bool
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
	add(capabilities.ManageMCP, "MCP", LocationMCP, "bi-tools")
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
			ID:          user.Binding.ID,
			ChannelType: user.Binding.ChannelType, Principal: user.Binding.Principal,
			DisplayName: user.Binding.DisplayName, ProviderUsername: user.Binding.ProviderUsername,
			ProviderFirstName: user.Binding.ProviderFirstName, Provenance: user.Binding.Provenance,
		}
	}
	bindings := user.Bindings
	if len(bindings) == 0 && user.Binding != nil {
		bindings = []usercmd.Binding{*user.Binding}
	}
	for _, binding := range bindings {
		view.Bindings = append(view.Bindings, BindingView{
			ID: binding.ID, ChannelType: binding.ChannelType, Principal: binding.Principal,
			DisplayName: binding.DisplayName, ProviderUsername: binding.ProviderUsername,
			ProviderFirstName: binding.ProviderFirstName, Provenance: binding.Provenance,
		})
	}
	return view
}

// ProjectSession constructs one family-level session row.
func ProjectSession(summary usercmd.SessionSummary, currentFamilyID string, now time.Time) SessionView {
	return SessionView{
		ID: summary.ID, Assurance: string(summary.Assurance), CreatedAt: summary.CreatedAt,
		LastSeenAt: summary.LastSeenAt, ExpiresAt: summary.ExpiresAt, RevokedAt: summary.RevokedAt,
		Revoked: !summary.RevokedAt.IsZero(), Expired: !now.Before(summary.ExpiresAt),
		Current: summary.ID == currentFamilyID, DeviceLabel: summary.DeviceLabel,
		ConnectionPeer: summary.ConnectionPeer, Version: summary.Version,
	}
}

// ProjectAudit constructs a safe audit row.
func ProjectAudit(event usercmd.AuditEvent) AuditView {
	actorName := "System"
	if event.ActorUserID != "" {
		actorName = "Former or unavailable user"
	}
	return AuditView{
		ID: safeAuditID(event.ID), Action: string(event.Action), Outcome: string(event.Outcome),
		ActionLabel: auditActionLabel(event.Action),
		ActorUserID: safeAuditID(event.ActorUserID), ActorName: actorName,
		ActorSessionID: safeAuditID(event.ActorSessionID),
		TargetType:     string(event.TargetType), TargetID: safeAuditID(event.TargetID),
		TargetName: auditTargetLabel(event.TargetType), OccurredAt: event.OccurredAt,
	}
}

func auditActionLabel(action usercmd.AuditAction) string {
	switch action {
	case usercmd.AuditActionUserCreated:
		return "Created user"
	case usercmd.AuditActionUserUpdated:
		return "Updated user"
	case usercmd.AuditActionUserAccessChanged, usercmd.AuditActionUserRoleChanged, usercmd.AuditActionUserStatusChanged:
		return "Changed user access"
	case usercmd.AuditActionCredentialChanged:
		return "Changed credential"
	case usercmd.AuditActionBindingAttached:
		return "Added chat binding"
	case usercmd.AuditActionBindingDetached:
		return "Removed chat binding"
	case usercmd.AuditActionBindingClaimCreated, usercmd.AuditActionInvitationIssued:
		return "Created binding invitation"
	case usercmd.AuditActionInvitationRevoked:
		return "Cancelled binding invitation"
	case usercmd.AuditActionUserMigrated:
		return "Migrated user"
	case usercmd.AuditActionLoginSucceeded:
		return "Signed in"
	case usercmd.AuditActionRefreshSucceeded:
		return "Restored browser session"
	case usercmd.AuditActionRefreshReplay:
		return "Detected session token replay"
	case usercmd.AuditActionSessionRevoked:
		return "Ended browser session"
	case usercmd.AuditActionMFAVerified:
		return "Verified passkey"
	case usercmd.AuditActionMFAEnabled:
		return "Enabled two-factor authentication"
	case usercmd.AuditActionMFAReplaced:
		return "Replaced passkey"
	case usercmd.AuditActionMFADisabled:
		return "Disabled two-factor authentication"
	case usercmd.AuditActionMFARecovered:
		return "Recovered two-factor access offline"
	case usercmd.AuditActionLogout:
		return "Signed out"
	default:
		return "Security event"
	}
}

func auditTargetLabel(target usercmd.AuditTargetType) string {
	switch target {
	case usercmd.AuditTargetUser:
		return "User"
	case usercmd.AuditTargetSession:
		return "Browser session"
	case usercmd.AuditTargetBinding:
		return "Chat binding"
	case usercmd.AuditTargetSystem:
		return "System"
	case usercmd.AuditTargetMigration:
		return "Migration"
	default:
		return "Target"
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
