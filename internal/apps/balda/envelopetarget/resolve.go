package envelopetarget

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/auth"
	"github.com/normahq/balda/internal/apps/balda/locatorref"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
)

const (
	TargetAlias   = "alias"
	AliasOwner    = "owner"
	TargetLocator = "locator"
)

type Target struct {
	Target string
	Key    string
}

type Resolved struct {
	Locator baldasession.SessionLocator
	UserID  string
	TopicID int
}

func Resolve(
	_ context.Context,
	ownerStore *auth.OwnerStore,
	target Target,
) (Resolved, error) {
	targetKind := strings.ToLower(strings.TrimSpace(target.Target))
	key := strings.TrimSpace(target.Key)
	if targetKind == "" {
		return Resolved{}, fmt.Errorf("envelope target is required")
	}
	if key == "" {
		return Resolved{}, fmt.Errorf("envelope target key is required")
	}

	switch targetKind {
	case TargetAlias:
		if strings.ToLower(key) != AliasOwner {
			return Resolved{}, fmt.Errorf("unsupported alias target %q", target.Key)
		}
		if ownerStore == nil {
			return Resolved{}, fmt.Errorf("owner store is required")
		}
		owner := ownerStore.GetOwner()
		if owner == nil {
			return Resolved{}, fmt.Errorf("owner is not registered")
		}
		if owner.UserID == 0 {
			return Resolved{}, fmt.Errorf("owner.user_id is required")
		}
		if owner.ChatID == 0 {
			return Resolved{}, fmt.Errorf("owner.chat_id is required")
		}

		return Resolved{
			Locator: telegramref.NewLocator(owner.ChatID, 0),
			UserID:  telegramref.UserID(owner.UserID),
			TopicID: 0,
		}, nil
	case TargetLocator:
		locator, err := locatorref.Parse(target.Key)
		if err != nil {
			return Resolved{}, err
		}
		resolved := Resolved{Locator: locator}
		if address, ok, decodeErr := telegramref.DecodeLocator(locator); decodeErr != nil {
			return Resolved{}, decodeErr
		} else if ok {
			resolved.TopicID = address.TopicID
		}
		return resolved, nil
	default:
		return Resolved{}, fmt.Errorf("unsupported envelope target %q", target.Target)
	}
}
