package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/client"
)

type telegramMeClient interface {
	GetMeWithResponse(ctx context.Context, reqEditors ...client.RequestEditorFn) (*client.GetMeResponse, error)
}

type botIdentity struct {
	name     string
	username string
}

func logBaldaStartup(ctx context.Context, botToken string) {
	identity, err := loadBotIdentityFromToken(ctx, botToken)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load bot identity for startup log")
	}

	event := log.Info()
	if identity.name != "" {
		event = event.Str("bot_name", identity.name)
	}
	if identity.username != "" {
		event = event.Str("bot_username", identity.username)
	}
	event.Msg("Balda bot started. Press Ctrl+C to stop.")
}

func loadBotIdentityFromToken(ctx context.Context, botToken string) (botIdentity, error) {
	tgClient, err := tgbotkit.NewClient(tgbotkit.Config{
		Token: strings.TrimSpace(botToken),
	})
	if err != nil {
		return botIdentity{}, fmt.Errorf("create Telegram client: %w", err)
	}
	return loadBotIdentity(ctx, tgClient)
}

func loadBotIdentity(ctx context.Context, tgClient telegramMeClient) (botIdentity, error) {
	resp, err := tgClient.GetMeWithResponse(ctx)
	if err != nil {
		return botIdentity{}, fmt.Errorf("get bot info: %w", err)
	}
	if resp == nil || resp.JSON200 == nil {
		return botIdentity{}, fmt.Errorf("getMe response missing result")
	}

	identity := botIdentity{
		name: strings.TrimSpace(resp.JSON200.Result.FirstName),
	}
	if resp.JSON200.Result.Username != nil {
		identity.username = strings.TrimSpace(*resp.JSON200.Result.Username)
	}
	if identity.username == "" {
		return botIdentity{}, fmt.Errorf("getMe response missing username")
	}

	return identity, nil
}
