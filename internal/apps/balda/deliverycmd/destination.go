package deliverycmd

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// RoleOwner identifies destinations with owner privileges and notifications.
	RoleOwner = "owner"
	// RoleCollaborator identifies destinations with collaborator privileges.
	RoleCollaborator = "collaborator"
)

var (
	// ErrDestinationNotFound indicates that no destination was configured or found for the alias.
	ErrDestinationNotFound = errors.New("destination not found")
	// ErrAmbiguousDestination indicates that multiple candidate destinations match without a default.
	ErrAmbiguousDestination = errors.New("ambiguous destination")
)

// AmbiguousDestinationError represents an ambiguous destination resolution error across multiple channels.
type AmbiguousDestinationError struct {
	Alias      string
	Candidates []string
}

func (e *AmbiguousDestinationError) Error() string {
	if len(e.Candidates) > 0 {
		return fmt.Sprintf("ambiguous destination for alias %q: multiple destinations configured across channels [%s]; specify a default or use a channel qualifier", e.Alias, strings.Join(e.Candidates, ", "))
	}
	return fmt.Sprintf("ambiguous destination for alias %q", e.Alias)
}

func (e *AmbiguousDestinationError) Is(target error) bool {
	return target == ErrAmbiguousDestination
}

// DestinationRecord represents a registered transport-neutral delivery destination
// that can be associated with roles (e.g. owner, collaborator) and designated as default.
type DestinationRecord struct {
	Locator     Locator   `json:"locator"`
	Principal   string    `json:"principal,omitempty"`
	Roles       []string  `json:"roles,omitempty"`
	ChannelType string    `json:"channel_type"`
	IsDefault   bool      `json:"is_default,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// LocatorRef returns the canonical locator ref string "<channel_type>:<address_key>".
func (d DestinationRecord) LocatorRef() string {
	channelType := strings.TrimSpace(d.ChannelType)
	if channelType == "" {
		channelType = strings.TrimSpace(d.Locator.ChannelType)
	}
	addressKey := strings.TrimSpace(d.Locator.AddressKey)
	if channelType == "" || addressKey == "" {
		return ""
	}
	return channelType + ":" + addressKey
}

// Validate validates that the destination record has a valid locator and channel type.
func (d DestinationRecord) Validate() error {
	channelType := strings.ToLower(strings.TrimSpace(d.ChannelType))
	if channelType == "" {
		return fmt.Errorf("destination channel_type is required")
	}
	locChannelType := strings.ToLower(strings.TrimSpace(d.Locator.ChannelType))
	if locChannelType == "" {
		return fmt.Errorf("destination locator channel_type is required")
	}
	if channelType != locChannelType {
		return fmt.Errorf("destination channel_type %q does not match locator channel_type %q", channelType, locChannelType)
	}
	if strings.TrimSpace(d.Locator.AddressKey) == "" {
		return fmt.Errorf("destination locator address_key is required")
	}
	if strings.TrimSpace(d.Locator.SessionID) == "" {
		return fmt.Errorf("destination locator session_id is required")
	}
	return nil
}

// HasRole checks whether the destination record is assigned the given role (case-insensitive).
func (d DestinationRecord) HasRole(role string) bool {
	target := strings.ToLower(strings.TrimSpace(role))
	if target == "" {
		return false
	}
	for _, r := range d.Roles {
		if strings.ToLower(strings.TrimSpace(r)) == target {
			return true
		}
	}
	return false
}

// NormalizeRoles normalizes a slice of roles by trimming, lowercasing, and deduplicating.
func NormalizeRoles(roles []string) []string {
	if len(roles) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(roles))
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		norm := strings.ToLower(strings.TrimSpace(r))
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}
