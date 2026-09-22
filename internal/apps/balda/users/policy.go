package users

import (
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// RoleStatusChange contains the state needed to authorize one role or status mutation.
type RoleStatusChange struct {
	Before                     usercmd.User
	NextRole                   usercmd.Role
	NextStatus                 usercmd.UserStatus
	ExpectedVersion            uint64
	ActiveAdministrators       int
	AcknowledgeBotAccessImpact bool
}

// CredentialChange contains the state needed to authorize one credential lifecycle mutation.
type CredentialChange struct {
	Before                    usercmd.User
	Next                      usercmd.Credential
	ExpectedUserVersion       uint64
	ExpectedCredentialVersion uint64
}

// BootstrapAction identifies whether bootstrap should use an existing user or create the first one.
type BootstrapAction string

const (
	// BootstrapUseExisting selects an existing active administrator.
	BootstrapUseExisting BootstrapAction = "use_existing"
	// BootstrapCreatePrimary creates the first unbound primary administrator.
	BootstrapCreatePrimary BootstrapAction = "create_primary"
)

// BootstrapSelection is the deterministic target of a bootstrap operation.
type BootstrapSelection struct {
	Action BootstrapAction
	UserID string
}

// BotCapability derives bot authorization exclusively from current user state.
func BotCapability(user usercmd.User) usercmd.BotCapability {
	if user.Status != usercmd.StatusActive || user.Binding == nil {
		return usercmd.BotCapabilityNone
	}
	switch user.Role {
	case usercmd.RoleAdministrator:
		return usercmd.BotCapabilityOwner
	case usercmd.RoleOperator:
		return usercmd.BotCapabilityCollaborator
	default:
		return usercmd.BotCapabilityNone
	}
}

// BackofficeCapabilities derives server-side navigation and mutation capabilities.
func BackofficeCapabilities(user usercmd.User) usercmd.BackofficeCapabilities {
	if user.Status != usercmd.StatusActive {
		return usercmd.BackofficeCapabilities{}
	}
	switch user.Role {
	case usercmd.RoleAdministrator:
		return usercmd.BackofficeCapabilities{
			Overview:    true,
			Account:     true,
			ManageUsers: true,
			ViewAudit:   true,
		}
	case usercmd.RoleOperator:
		return usercmd.BackofficeCapabilities{Overview: true, Account: true}
	default:
		return usercmd.BackofficeCapabilities{}
	}
}

// AuthenticationAssurance maps valid local credential state to session assurance.
func AuthenticationAssurance(user usercmd.User) (usercmd.SessionAssurance, error) {
	if err := ValidateUser(user); err != nil {
		return "", err
	}
	if user.Status != usercmd.StatusActive || user.Credential.State == usercmd.CredentialStateDisabled {
		return "", usercmd.ErrAuthenticationDisabled
	}
	if user.Credential.State == usercmd.CredentialStateTemporary {
		return usercmd.SessionAssuranceRestricted, nil
	}
	return usercmd.SessionAssuranceNormal, nil
}

// ValidateUser checks the canonical identity and credential-state invariants.
func ValidateUser(user usercmd.User) error {
	if strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.DisplayName) == "" {
		return fmt.Errorf("%w: user identity and display name are required", usercmd.ErrInvalid)
	}
	if !user.Role.Valid() || !user.Status.Valid() {
		return fmt.Errorf("%w: user role or status is unsupported", usercmd.ErrInvalid)
	}
	username := strings.TrimSpace(user.Username)
	if username == "" || user.NormalizedUsername != NormalizeUsername(username) {
		return fmt.Errorf("%w: normalized username is required", usercmd.ErrInvalid)
	}
	if user.Version == 0 {
		return fmt.Errorf("%w: user version must be positive", usercmd.ErrInvalid)
	}
	if err := validateCredential(user.Credential); err != nil {
		return err
	}
	if user.Primary && user.Role != usercmd.RoleAdministrator {
		return fmt.Errorf("%w: primary user must be an administrator", usercmd.ErrInvalid)
	}
	if user.Binding != nil {
		if err := validateBinding(*user.Binding); err != nil {
			return err
		}
		if user.Binding.UserID != user.ID {
			return fmt.Errorf("%w: binding owner does not match user", usercmd.ErrInvalid)
		}
	}
	return nil
}

// ValidateCredentialChange checks optimistic versions and a monotonic credential transition.
func ValidateCredentialChange(change CredentialChange) error {
	if err := ValidateUser(change.Before); err != nil {
		return err
	}
	if change.ExpectedUserVersion != change.Before.Version ||
		change.ExpectedCredentialVersion != change.Before.Credential.Version {
		return usercmd.ErrConflict
	}
	if err := validateCredential(change.Next); err != nil {
		return err
	}
	if change.Next.Version != change.Before.Credential.Version+1 {
		return fmt.Errorf("%w: credential version must increase by one", usercmd.ErrInvalid)
	}
	return nil
}

// NormalizeUsername returns the canonical comparison form for a local username.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ValidateBindingAssignment enforces one binding per user and one owner per principal.
func ValidateBindingAssignment(user usercmd.User, candidate usercmd.Binding, existing *usercmd.Binding) error {
	if err := ValidateUser(user); err != nil {
		return err
	}
	if err := validateBinding(candidate); err != nil {
		return err
	}
	if candidate.UserID != user.ID {
		return fmt.Errorf("%w: binding owner does not match user", usercmd.ErrInvalid)
	}
	if user.Binding != nil {
		return usercmd.ErrBindingAlreadyAssigned
	}
	if existing != nil {
		return usercmd.ErrBindingPrincipalInUse
	}
	return nil
}

// ValidateRoleStatusChange checks versions, last-administrator safety, and bot impact acknowledgement.
func ValidateRoleStatusChange(change RoleStatusChange) error {
	if err := ValidateUser(change.Before); err != nil {
		return err
	}
	if change.ExpectedVersion != change.Before.Version {
		return usercmd.ErrConflict
	}
	if !change.NextRole.Valid() || !change.NextStatus.Valid() {
		return fmt.Errorf("%w: next role or status is unsupported", usercmd.ErrInvalid)
	}
	after := change.Before
	after.Role = change.NextRole
	after.Status = change.NextStatus
	if change.Before.Role == usercmd.RoleAdministrator && change.Before.Status == usercmd.StatusActive &&
		(after.Role != usercmd.RoleAdministrator || after.Status != usercmd.StatusActive) && change.ActiveAdministrators <= 1 {
		return usercmd.ErrLastAdministrator
	}
	if err := ValidateUser(after); err != nil {
		return err
	}
	if BotCapability(change.Before) != BotCapability(after) && !change.AcknowledgeBotAccessImpact {
		return usercmd.ErrBotImpactAcknowledgementRequired
	}
	return nil
}

// ValidatePrimaryUsers checks that primary selection is unique and administrative.
func ValidatePrimaryUsers(users []usercmd.User) error {
	primaryCount := 0
	for _, user := range users {
		if err := ValidateUser(user); err != nil {
			return err
		}
		if user.Primary {
			primaryCount++
		}
	}
	if primaryCount > 1 {
		return usercmd.ErrPrimaryConflict
	}
	return nil
}

// SelectBootstrapUser deterministically selects the explicit or primary administrator.
func SelectBootstrapUser(users []usercmd.User, explicitUserID string) (BootstrapSelection, error) {
	if len(users) == 0 {
		if strings.TrimSpace(explicitUserID) != "" {
			return BootstrapSelection{}, usercmd.ErrNotFound
		}
		return BootstrapSelection{Action: BootstrapCreatePrimary}, nil
	}
	if err := ValidatePrimaryUsers(users); err != nil {
		return BootstrapSelection{}, err
	}
	if explicit := strings.TrimSpace(explicitUserID); explicit != "" {
		for _, user := range users {
			if user.ID != explicit {
				continue
			}
			if user.Role != usercmd.RoleAdministrator || user.Status != usercmd.StatusActive {
				return BootstrapSelection{}, fmt.Errorf("%w: bootstrap target must be an active administrator", usercmd.ErrInvalid)
			}
			return BootstrapSelection{Action: BootstrapUseExisting, UserID: user.ID}, nil
		}
		return BootstrapSelection{}, usercmd.ErrNotFound
	}
	for _, user := range users {
		if user.Primary && user.Role == usercmd.RoleAdministrator && user.Status == usercmd.StatusActive {
			return BootstrapSelection{Action: BootstrapUseExisting, UserID: user.ID}, nil
		}
	}
	return BootstrapSelection{}, usercmd.ErrBootstrapSelectionRequired
}

// ValidateBindingClaim checks single-use claim lifetime and user/channel scope.
func ValidateBindingClaim(claim usercmd.BindingClaim, userID, channelType string, now time.Time) error {
	if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.UserID) == "" || strings.TrimSpace(claim.ChannelType) == "" || claim.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: binding claim is incomplete", usercmd.ErrInvalid)
	}
	if claim.UserID != strings.TrimSpace(userID) || claim.ChannelType != strings.TrimSpace(channelType) {
		return usercmd.ErrBindingClaimScope
	}
	if !claim.ConsumedAt.IsZero() || !now.Before(claim.ExpiresAt) {
		return usercmd.ErrBindingClaimUnavailable
	}
	return nil
}

// ValidateSessionRevocation prevents accidental revocation of the current browser session.
func ValidateSessionRevocation(currentSessionID, targetSessionID string, confirmCurrent bool) error {
	current := strings.TrimSpace(currentSessionID)
	target := strings.TrimSpace(targetSessionID)
	if current == "" || target == "" {
		return fmt.Errorf("%w: current and target session IDs are required", usercmd.ErrInvalid)
	}
	if current == target && !confirmCurrent {
		return usercmd.ErrCurrentSessionConfirmationRequired
	}
	return nil
}

func validateBinding(binding usercmd.Binding) error {
	if strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.UserID) == "" || strings.TrimSpace(binding.ChannelType) == "" || strings.TrimSpace(binding.Principal) == "" {
		return fmt.Errorf("%w: binding identity, owner, channel, and principal are required", usercmd.ErrInvalid)
	}
	return nil
}

func validateCredential(credential usercmd.Credential) error {
	if !credential.State.Valid() || credential.Version == 0 {
		return fmt.Errorf("%w: credential state and version are required", usercmd.ErrInvalid)
	}
	if (credential.State == usercmd.CredentialStateTemporary) != credential.MustChange {
		return fmt.Errorf("%w: temporary credential must require password change", usercmd.ErrInvalid)
	}
	return nil
}
