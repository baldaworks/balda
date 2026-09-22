package usercmd

import (
	"errors"
	"testing"
	"time"
)

func TestValidateSessionFamily(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	family := SessionFamily{
		ID: "session-1", UserID: "user-1", Assurance: SessionAssuranceNormal, CredentialVersion: 3,
		Access:             AccessCredential{Selector: "access-selector", VerifierDigest: []byte("access-digest"), ExpiresAt: now.Add(time.Minute)},
		CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour), Version: 1,
		RefreshTokens: []RefreshToken{
			{Selector: "old-refresh", VerifierDigest: []byte("old-digest"), Generation: 1, State: RefreshTokenStateUsed, IssuedAt: now, UsedAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)},
			{Selector: "active-refresh", VerifierDigest: []byte("active-digest"), Generation: 2, State: RefreshTokenStateActive, IssuedAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)},
		},
	}

	if err := ValidateSessionFamily(family); err != nil {
		t.Fatalf("ValidateSessionFamily() error = %v, want nil", err)
	}

	missingActive := family
	missingActive.RefreshTokens = missingActive.RefreshTokens[:1]
	if err := ValidateSessionFamily(missingActive); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing active refresh error = %v, want %v", err, ErrInvalid)
	}

	twoActive := family
	twoActive.RefreshTokens = append([]RefreshToken(nil), family.RefreshTokens...)
	twoActive.RefreshTokens[0].State = RefreshTokenStateActive
	twoActive.RefreshTokens[0].UsedAt = time.Time{}
	if err := ValidateSessionFamily(twoActive); !errors.Is(err, ErrInvalid) {
		t.Errorf("two active refresh tokens error = %v, want %v", err, ErrInvalid)
	}

	shortenedLifetime := family
	shortenedLifetime.RefreshTokens = append([]RefreshToken(nil), family.RefreshTokens...)
	shortenedLifetime.RefreshTokens[1].ExpiresAt = now.Add(30 * time.Minute)
	if err := ValidateSessionFamily(shortenedLifetime); !errors.Is(err, ErrInvalid) {
		t.Errorf("shortened refresh lifetime error = %v, want %v", err, ErrInvalid)
	}
}

func TestValidateAuditEvent(t *testing.T) {
	t.Parallel()

	event := AuditEvent{
		ID: "audit-1", Action: AuditActionUserRoleChanged, Outcome: AuditOutcomeSucceeded,
		ActorUserID: "actor-1", TargetType: AuditTargetUser, TargetID: "user-1",
		Source: "backoffice", OccurredAt: time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC),
	}
	if err := ValidateAuditEvent(event); err != nil {
		t.Fatalf("ValidateAuditEvent() error = %v, want nil", err)
	}

	event.Action = ""
	if err := ValidateAuditEvent(event); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing action error = %v, want %v", err, ErrInvalid)
	}

	event.Action = AuditActionUserRoleChanged
	event.Outcome = AuditOutcome("unsupported")
	if err := ValidateAuditEvent(event); !errors.Is(err, ErrInvalid) {
		t.Errorf("unsupported outcome error = %v, want %v", err, ErrInvalid)
	}

	event.Outcome = AuditOutcomeSucceeded
	event.Reason = string(make([]byte, 1025))
	if err := ValidateAuditEvent(event); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized reason error = %v, want %v", err, ErrInvalid)
	}
}
