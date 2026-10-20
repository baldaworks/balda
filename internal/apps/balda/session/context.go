package session

import (
	"fmt"
	"strings"
)

// Kind identifies the execution policy owned by an active session.
type Kind string

const (
	KindDM       Kind = "dm"
	KindChat     Kind = "chat"
	KindSchedule Kind = "schedule"
	KindWebhook  Kind = "webhook"
)

// Family is the policy family derived from a Kind.
type Family string

const (
	FamilyConversation Family = "conversation"
	FamilyService      Family = "service"
)

// KindForConversation returns the authoritative kind for trusted transport
// scope metadata.
func KindForConversation(direct bool) Kind {
	if direct {
		return KindDM
	}
	return KindChat
}

// Family derives the policy family for a supported kind.
func (k Kind) Family() Family {
	switch k {
	case KindDM, KindChat:
		return FamilyConversation
	case KindSchedule, KindWebhook:
		return FamilyService
	default:
		return ""
	}
}

func validateExecutionContext(kind Kind, ownerJobID string) error {
	ownerJobID = strings.TrimSpace(ownerJobID)
	switch kind.Family() {
	case FamilyConversation:
		if ownerJobID != "" {
			return fmt.Errorf("conversation session cannot have an owner job")
		}
	case FamilyService:
		if ownerJobID == "" {
			return fmt.Errorf("service session requires an owner job")
		}
	default:
		return fmt.Errorf("unsupported session kind %q", kind)
	}
	return nil
}

// SessionContext carries the channel locator plus the transport actor identity
// used to bind the underlying runtime session.
type SessionContext struct {
	Locator                    SessionLocator
	UserID                     string
	Kind                       Kind
	OwnerJobID                 string
	AllowBaldaProviderFallback bool
}

// KindResolver reconstructs conversation kind from a canonical transport
// locator when durable legacy metadata does not carry execution context.
type KindResolver interface {
	ResolveKind(locator SessionLocator) (Kind, error)
}
