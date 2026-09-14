package commandfx

import (
	"context"
	"regexp"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
)

var (
	telegramCommandPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	textCommandPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// RegistryAdvertisementTarget projects provider-compatible aliases into the
// neutral registry consumed by concrete transport parsers.
type RegistryAdvertisementTarget struct {
	transport string
	registry  *commandcmd.Registry
}

// NewRegistryAdvertisementTarget creates one transport projection adapter.
func NewRegistryAdvertisementTarget(transport string, registry *commandcmd.Registry) *RegistryAdvertisementTarget {
	return &RegistryAdvertisementTarget{transport: strings.ToLower(strings.TrimSpace(transport)), registry: registry}
}

func (t *RegistryAdvertisementTarget) Transport() string { return t.transport }
func (t *RegistryAdvertisementTarget) SupportsCommand(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if t.transport == "telegram" {
		return telegramCommandPattern.MatchString(name)
	}
	return textCommandPattern.MatchString(name)
}
func (t *RegistryAdvertisementTarget) ReplaceCommands(_ context.Context, projection commandcmd.AdvertisementProjection) error {
	t.registry.Replace(t.transport, projection)
	return nil
}

var _ AdvertisementTarget = (*RegistryAdvertisementTarget)(nil)
