package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

const baldaOwnerAuthTokenKV = "owner_auth_token"

var baldaGenerateOwnerToken = auth.GenerateOwnerToken

func loadOrCreateBaldaOwnerToken(ctx context.Context, dbPath string) (string, error) {
	provider, err := baldastate.NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		return "", fmt.Errorf("open balda state provider: %w", err)
	}
	defer func() { _ = provider.Close() }()

	stored, ok, err := provider.AppKV().Get(ctx, baldaOwnerAuthTokenKV)
	if err != nil {
		return "", fmt.Errorf("read owner auth token: %w", err)
	}
	if ok {
		trimmed := strings.TrimSpace(stored)
		if trimmed != "" {
			return trimmed, nil
		}
	}

	token, err := baldaGenerateOwnerToken()
	if err != nil {
		return "", fmt.Errorf("generate owner auth token: %w", err)
	}
	if err := provider.AppKV().Set(ctx, baldaOwnerAuthTokenKV, token); err != nil {
		return "", fmt.Errorf("store owner auth token: %w", err)
	}

	return token, nil
}
