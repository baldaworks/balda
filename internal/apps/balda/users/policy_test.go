package users

import (
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestBotCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user usercmd.User
		want usercmd.BotCapability
	}{
		{
			name: "bound administrator",
			user: testUser(usercmd.RoleAdministrator, usercmd.StatusActive, testBinding("telegram", "101")),
			want: usercmd.BotCapabilityOwner,
		},
		{
			name: "bound operator",
			user: testUser(usercmd.RoleOperator, usercmd.StatusActive, testBinding("telegram", "102")),
			want: usercmd.BotCapabilityCollaborator,
		},
		{
			name: "unbound administrator",
			user: testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil),
			want: usercmd.BotCapabilityNone,
		},
		{
			name: "disabled bound administrator",
			user: testUser(usercmd.RoleAdministrator, usercmd.StatusDisabled, testBinding("telegram", "103")),
			want: usercmd.BotCapabilityNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := BotCapability(tt.user); got != tt.want {
				t.Errorf("BotCapability() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackofficeCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user usercmd.User
		want usercmd.BackofficeCapabilities
	}{
		{
			name: "administrator",
			user: testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil),
			want: usercmd.BackofficeCapabilities{Overview: true, Account: true, ManageUsers: true, ViewAudit: true},
		},
		{
			name: "operator",
			user: testUser(usercmd.RoleOperator, usercmd.StatusActive, nil),
			want: usercmd.BackofficeCapabilities{Overview: true, Account: true},
		},
		{
			name: "disabled",
			user: testUser(usercmd.RoleAdministrator, usercmd.StatusDisabled, nil),
		},
		{
			name: "unsupported role",
			user: func() usercmd.User {
				u := testUser(usercmd.RoleOperator, usercmd.StatusActive, nil)
				u.Role = usercmd.Role("unsupported")
				return u
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := BackofficeCapabilities(tt.user); got != tt.want {
				t.Errorf("BackofficeCapabilities() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAuthenticationAssurance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    usercmd.User
		want    usercmd.SessionAssurance
		wantErr error
	}{
		{
			name: "active credential",
			user: testCredentialUser(usercmd.CredentialStateActive, false),
			want: usercmd.SessionAssuranceNormal,
		},
		{
			name: "temporary credential",
			user: testCredentialUser(usercmd.CredentialStateTemporary, true),
			want: usercmd.SessionAssuranceRestricted,
		},
		{
			name:    "disabled credential",
			user:    testCredentialUser(usercmd.CredentialStateDisabled, false),
			wantErr: usercmd.ErrAuthenticationDisabled,
		},
		{
			name: "disabled user",
			user: func() usercmd.User {
				u := testCredentialUser(usercmd.CredentialStateActive, false)
				u.Status = usercmd.StatusDisabled
				return u
			}(),
			wantErr: usercmd.ErrAuthenticationDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AuthenticationAssurance(tt.user)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AuthenticationAssurance() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("AuthenticationAssurance() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateUserCredentialState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   usercmd.CredentialState
		must    bool
		wantErr error
	}{
		{name: "temporary must change", state: usercmd.CredentialStateTemporary, must: true},
		{name: "active does not require change", state: usercmd.CredentialStateActive, must: false},
		{name: "disabled does not require change", state: usercmd.CredentialStateDisabled, must: false},
		{name: "temporary without change", state: usercmd.CredentialStateTemporary, must: false, wantErr: usercmd.ErrInvalid},
		{name: "active with change", state: usercmd.CredentialStateActive, must: true, wantErr: usercmd.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := testCredentialUser(tt.state, tt.must)
			err := ValidateUser(u)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateUser() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCredentialChange(t *testing.T) {
	t.Parallel()

	user := testCredentialUser(usercmd.CredentialStateActive, false)
	user.Version = 4
	user.Credential.Version = 3

	valid := CredentialChange{
		Before:                    user,
		Next:                      usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true, Version: 4},
		ExpectedUserVersion:       4,
		ExpectedCredentialVersion: 3,
	}
	if err := ValidateCredentialChange(valid); err != nil {
		t.Fatalf("ValidateCredentialChange() error = %v, want nil", err)
	}

	stale := valid
	stale.ExpectedCredentialVersion = 2
	if err := ValidateCredentialChange(stale); !errors.Is(err, usercmd.ErrConflict) {
		t.Errorf("stale credential version error = %v, want %v", err, usercmd.ErrConflict)
	}

	nonMonotonic := valid
	nonMonotonic.Next.Version = 3
	if err := ValidateCredentialChange(nonMonotonic); !errors.Is(err, usercmd.ErrInvalid) {
		t.Errorf("non-monotonic credential version error = %v, want %v", err, usercmd.ErrInvalid)
	}

	inconsistent := valid
	inconsistent.Next.MustChange = false
	if err := ValidateCredentialChange(inconsistent); !errors.Is(err, usercmd.ErrInvalid) {
		t.Errorf("inconsistent credential state error = %v, want %v", err, usercmd.ErrInvalid)
	}
}

func TestValidateBindingAssignment(t *testing.T) {
	t.Parallel()

	user := testUser(usercmd.RoleOperator, usercmd.StatusActive, nil)
	candidate := *testBinding("telegram", "201")
	if err := ValidateBindingAssignment(user, candidate, nil); err != nil {
		t.Fatalf("ValidateBindingAssignment() error = %v, want nil", err)
	}

	bound := user
	bound.Binding = testBinding("zulip", "202")
	if err := ValidateBindingAssignment(bound, candidate, nil); !errors.Is(err, usercmd.ErrBindingAlreadyAssigned) {
		t.Errorf("bound user error = %v, want %v", err, usercmd.ErrBindingAlreadyAssigned)
	}

	existing := candidate
	existing.UserID = "another-user"
	if err := ValidateBindingAssignment(user, candidate, &existing); !errors.Is(err, usercmd.ErrBindingPrincipalInUse) {
		t.Errorf("used principal error = %v, want %v", err, usercmd.ErrBindingPrincipalInUse)
	}
}

func TestValidateRoleStatusChange(t *testing.T) {
	t.Parallel()

	boundAdmin := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, testBinding("telegram", "301"))
	boundAdmin.Version = 7

	tests := []struct {
		name    string
		change  RoleStatusChange
		wantErr error
	}{
		{
			name: "acknowledged bot access change",
			change: RoleStatusChange{
				Before: boundAdmin, NextRole: usercmd.RoleOperator, NextStatus: usercmd.StatusActive,
				ExpectedVersion: 7, ActiveAdministrators: 2, AcknowledgeBotAccessImpact: true,
			},
		},
		{
			name: "missing bot access acknowledgement",
			change: RoleStatusChange{
				Before: boundAdmin, NextRole: usercmd.RoleOperator, NextStatus: usercmd.StatusActive,
				ExpectedVersion: 7, ActiveAdministrators: 2,
			},
			wantErr: usercmd.ErrBotImpactAcknowledgementRequired,
		},
		{
			name: "last active administrator",
			change: RoleStatusChange{
				Before: boundAdmin, NextRole: usercmd.RoleOperator, NextStatus: usercmd.StatusActive,
				ExpectedVersion: 7, ActiveAdministrators: 1, AcknowledgeBotAccessImpact: true,
			},
			wantErr: usercmd.ErrLastAdministrator,
		},
		{
			name: "optimistic conflict",
			change: RoleStatusChange{
				Before: boundAdmin, NextRole: usercmd.RoleAdministrator, NextStatus: usercmd.StatusActive,
				ExpectedVersion: 6, ActiveAdministrators: 1,
			},
			wantErr: usercmd.ErrConflict,
		},
		{
			name: "unbound role change needs no bot acknowledgement",
			change: RoleStatusChange{
				Before:   func() usercmd.User { u := boundAdmin; u.Binding = nil; return u }(),
				NextRole: usercmd.RoleOperator, NextStatus: usercmd.StatusActive,
				ExpectedVersion: 7, ActiveAdministrators: 2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRoleStatusChange(tt.change)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateRoleStatusChange() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRoleStatusChangeRejectsPrimaryAdministratorDemotion(t *testing.T) {
	user := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	user.Primary = true

	err := ValidateRoleStatusChange(RoleStatusChange{
		Before:                     user,
		NextRole:                   usercmd.RoleOperator,
		NextStatus:                 usercmd.StatusActive,
		ExpectedVersion:            user.Version,
		ActiveAdministrators:       2,
		AcknowledgeBotAccessImpact: true,
	})
	if !errors.Is(err, usercmd.ErrInvalid) {
		t.Fatalf("ValidateRoleStatusChange() error = %v, want ErrInvalid", err)
	}
}

func TestValidatePrimaryUsersRejectsMultiplePrimaryUsers(t *testing.T) {
	first := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	first.Primary = true
	second := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	second.ID = "user-2"
	second.Username = "second"
	second.NormalizedUsername = "second"
	second.Primary = true

	err := ValidatePrimaryUsers([]usercmd.User{first, second})
	if !errors.Is(err, usercmd.ErrPrimaryConflict) {
		t.Fatalf("ValidatePrimaryUsers() error = %v, want ErrPrimaryConflict", err)
	}
}

func TestSelectBootstrapUser(t *testing.T) {
	t.Parallel()

	primary := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	primary.ID = "primary"
	primary.Primary = true
	secondary := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	secondary.ID = "secondary"

	selection, err := SelectBootstrapUser([]usercmd.User{primary, secondary}, "")
	if err != nil {
		t.Fatalf("SelectBootstrapUser(primary) error = %v", err)
	}
	if selection.Action != BootstrapUseExisting || selection.UserID != primary.ID {
		t.Errorf("SelectBootstrapUser(primary) = %+v", selection)
	}

	selection, err = SelectBootstrapUser([]usercmd.User{primary, secondary}, secondary.ID)
	if err != nil {
		t.Fatalf("SelectBootstrapUser(explicit) error = %v", err)
	}
	if selection.UserID != secondary.ID {
		t.Errorf("SelectBootstrapUser(explicit).UserID = %q, want %q", selection.UserID, secondary.ID)
	}

	selection, err = SelectBootstrapUser(nil, "")
	if err != nil {
		t.Fatalf("SelectBootstrapUser(empty) error = %v", err)
	}
	if selection.Action != BootstrapCreatePrimary {
		t.Errorf("SelectBootstrapUser(empty).Action = %q, want %q", selection.Action, BootstrapCreatePrimary)
	}
}

func TestValidateBindingClaim(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	claim := usercmd.BindingClaim{
		ID: "claim-1", UserID: "user-1", ChannelType: "telegram", ExpiresAt: now.Add(time.Minute),
	}
	if err := ValidateBindingClaim(claim, "user-1", "telegram", now); err != nil {
		t.Fatalf("ValidateBindingClaim() error = %v, want nil", err)
	}

	consumed := claim
	consumed.ConsumedAt = now
	if err := ValidateBindingClaim(consumed, "user-1", "telegram", now); !errors.Is(err, usercmd.ErrBindingClaimUnavailable) {
		t.Errorf("consumed claim error = %v, want %v", err, usercmd.ErrBindingClaimUnavailable)
	}
	if err := ValidateBindingClaim(claim, "user-1", "zulip", now); !errors.Is(err, usercmd.ErrBindingClaimScope) {
		t.Errorf("wrong channel error = %v, want %v", err, usercmd.ErrBindingClaimScope)
	}
	if err := ValidateBindingClaim(claim, "user-1", "telegram", claim.ExpiresAt); !errors.Is(err, usercmd.ErrBindingClaimUnavailable) {
		t.Errorf("expired claim error = %v, want %v", err, usercmd.ErrBindingClaimUnavailable)
	}
}

func TestValidateSessionRevocation(t *testing.T) {
	t.Parallel()

	if err := ValidateSessionRevocation("session-1", "session-2", false); err != nil {
		t.Fatalf("ValidateSessionRevocation(other) error = %v", err)
	}
	if err := ValidateSessionRevocation("session-1", "session-1", false); !errors.Is(err, usercmd.ErrCurrentSessionConfirmationRequired) {
		t.Errorf("current session error = %v, want %v", err, usercmd.ErrCurrentSessionConfirmationRequired)
	}
	if err := ValidateSessionRevocation("session-1", "session-1", true); err != nil {
		t.Errorf("confirmed current session error = %v, want nil", err)
	}
}

func testCredentialUser(state usercmd.CredentialState, mustChange bool) usercmd.User {
	u := testUser(usercmd.RoleAdministrator, usercmd.StatusActive, nil)
	u.Credential = usercmd.Credential{State: state, MustChange: mustChange, Version: 1}
	return u
}

func testUser(role usercmd.Role, status usercmd.UserStatus, binding *usercmd.Binding) usercmd.User {
	return usercmd.User{
		ID: "user-1", DisplayName: "User One", Username: "user.one", NormalizedUsername: "user.one",
		Role: role, Status: status, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
		Binding: binding, Version: 1,
	}
}

func testBinding(channelType, principal string) *usercmd.Binding {
	return &usercmd.Binding{ID: "binding-1", UserID: "user-1", ChannelType: channelType, Principal: principal}
}
