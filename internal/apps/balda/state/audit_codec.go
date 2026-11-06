package state

import (
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	legacyAuditActionWebAuthRevoked          usercmd.AuditAction     = "session.revoked"
	legacyAuditActionWebAuthLoginSucceeded   usercmd.AuditAction     = "session.login.succeeded"
	legacyAuditActionWebAuthRefreshSucceeded usercmd.AuditAction     = "session.refresh.succeeded"
	legacyAuditActionWebAuthRefreshReplay    usercmd.AuditAction     = "session.refresh.replay"
	legacyAuditActionWebAuthMFAVerified      usercmd.AuditAction     = "session.mfa.verified"
	legacyAuditActionWebAuthLogout           usercmd.AuditAction     = "session.logout"
	legacyAuditTargetWebAuthSession          usercmd.AuditTargetType = "session"
)

func decodeStoredAudit(event usercmd.AuditEvent) (usercmd.AuditEvent, error) {
	legacy := false
	switch event.Action {
	case legacyAuditActionWebAuthRevoked:
		event.Action, legacy = usercmd.AuditActionWebAuthRevoked, true
	case legacyAuditActionWebAuthLoginSucceeded:
		event.Action, legacy = usercmd.AuditActionWebAuthLoginSucceeded, true
	case legacyAuditActionWebAuthRefreshSucceeded:
		event.Action, legacy = usercmd.AuditActionWebAuthRefreshSucceeded, true
	case legacyAuditActionWebAuthRefreshReplay:
		event.Action, legacy = usercmd.AuditActionWebAuthRefreshReplay, true
	case legacyAuditActionWebAuthMFAVerified:
		event.Action, legacy = usercmd.AuditActionWebAuthMFAVerified, true
	case legacyAuditActionWebAuthLogout:
		event.Action, legacy = usercmd.AuditActionWebAuthLogout, true
	}
	if event.TargetType == legacyAuditTargetWebAuthSession {
		event.TargetType, legacy = usercmd.AuditTargetWebAuthSession, true
	}
	if legacy {
		event.Encoding = usercmd.AuditEncodingLegacyWebAuth
	} else {
		event.Encoding = usercmd.AuditEncodingCanonical
	}
	if err := usercmd.ValidateAuditEvent(event); err != nil {
		return usercmd.AuditEvent{}, fmt.Errorf("decode stored audit: %w", err)
	}
	return event, nil
}

func encodeStoredAuditLegacy(event usercmd.AuditEvent) (usercmd.AuditEvent, error) {
	if err := usercmd.ValidateAuditEvent(event); err != nil {
		return usercmd.AuditEvent{}, fmt.Errorf("encode stored audit: %w", err)
	}
	switch event.Action {
	case usercmd.AuditActionWebAuthRevoked:
		event.Action = legacyAuditActionWebAuthRevoked
	case usercmd.AuditActionWebAuthLoginSucceeded:
		event.Action = legacyAuditActionWebAuthLoginSucceeded
	case usercmd.AuditActionWebAuthRefreshSucceeded:
		event.Action = legacyAuditActionWebAuthRefreshSucceeded
	case usercmd.AuditActionWebAuthRefreshReplay:
		event.Action = legacyAuditActionWebAuthRefreshReplay
	case usercmd.AuditActionWebAuthMFAVerified:
		event.Action = legacyAuditActionWebAuthMFAVerified
	case usercmd.AuditActionWebAuthLogout:
		event.Action = legacyAuditActionWebAuthLogout
	}
	if event.TargetType == usercmd.AuditTargetWebAuthSession {
		event.TargetType = legacyAuditTargetWebAuthSession
	}
	event.Encoding = ""
	return event, nil
}
