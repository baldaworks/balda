package mattermostfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/rs/zerolog"
)

// NewClient builds the Mattermost REST client from configuration values.
//
// Validation is deliberate: when the transport is enabled, a missing or
// malformed server URL / token must fail at startup rather than on the first
// delivery attempt.
func NewClient(serverURL, token, botUserID string, enabled bool, logger zerolog.Logger) (*mattermost.Client, error) {
	if !enabled {
		// A disabled transport still receives a client so that fx graph
		// construction and adapter tests do not need conditional wiring.
		return mattermost.NewClient(serverURL, token, botUserID), nil
	}
	if err := mattermost.ValidateConfig(serverURL, token); err != nil {
		return nil, err
	}
	logger.Info().
		Str("server_url", serverURL).
		Bool("bot_user_id_set", botUserID != "").
		Msg("mattermost transport configured")
	return mattermost.NewClient(serverURL, token, botUserID), nil
}
