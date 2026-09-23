package usercmd

import "errors"

var (
	// ErrInvalid identifies a contract value that violates a user invariant.
	ErrInvalid = errors.New("invalid user contract")
	// ErrNotFound identifies a requested canonical user or session that does not exist.
	ErrNotFound = errors.New("user resource not found")
	// ErrConflict identifies an optimistic version conflict.
	ErrConflict = errors.New("user version conflict")
	// ErrForbidden identifies an authenticated actor without the required user capability.
	ErrForbidden = errors.New("user operation forbidden")
	// ErrAuthenticationDisabled identifies a user or credential that cannot authenticate.
	ErrAuthenticationDisabled = errors.New("authentication disabled")
	// ErrBindingAlreadyAssigned identifies a user that already has its single binding.
	ErrBindingAlreadyAssigned = errors.New("user already has a transport binding")
	// ErrBindingPrincipalInUse identifies a transport principal owned by another user.
	ErrBindingPrincipalInUse = errors.New("transport principal already belongs to a user")
	// ErrBindingClaimUnavailable identifies a consumed or expired binding claim.
	ErrBindingClaimUnavailable = errors.New("binding claim unavailable")
	// ErrBindingClaimScope identifies a binding claim used outside its user or channel scope.
	ErrBindingClaimScope = errors.New("binding claim scope mismatch")
	// ErrBotImpactAcknowledgementRequired identifies an unacknowledged bot access change.
	ErrBotImpactAcknowledgementRequired = errors.New("bot access impact acknowledgement required")
	// ErrLastAdministrator identifies a mutation that would remove the last active administrator.
	ErrLastAdministrator = errors.New("at least one active administrator is required")
	// ErrPrimaryConflict identifies invalid primary-user cardinality.
	ErrPrimaryConflict = errors.New("at most one primary user is allowed")
	// ErrBootstrapSelectionRequired identifies an existing store without a usable bootstrap target.
	ErrBootstrapSelectionRequired = errors.New("bootstrap user selection required")
	// ErrCurrentSessionConfirmationRequired identifies accidental current-session revocation.
	ErrCurrentSessionConfirmationRequired = errors.New("current session revocation confirmation required")
	// ErrSessionUnavailable identifies an expired, revoked, stale, or otherwise unusable browser session.
	ErrSessionUnavailable = errors.New("browser session unavailable")
)
