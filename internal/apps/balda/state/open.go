package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// Open opens exactly the backend selected by a resolved database configuration.
// Call Resolve once before Open; the SQLite path is never rendered here.
func Open(ctx context.Context, cfg DatabaseConfig) (Provider, error) {
	if cfg.Type == databaseSQLite {
		if !filepath.IsAbs(cfg.SQLite.Path) {
			return nil, fmt.Errorf("sqlite database path must be resolved before opening state")
		}
		if err := os.MkdirAll(filepath.Dir(cfg.SQLite.Path), 0o700); err != nil {
			return nil, fmt.Errorf("create sqlite database directory: %w", err)
		}
	}
	log.Info().Str("database_type", cfg.Type).Msg("opening Balda state database")
	p, err := openProvider(ctx, cfg, NewSQLiteProvider, NewPostgresProvider)
	if err != nil {
		return nil, err
	}
	log.Info().Str("database_type", cfg.Type).Msg("Balda state database ready")
	return p, nil
}

// openProvider dispatches already-resolved configuration. Constructors are
// supplied by the composition entrypoint so selection can be tested without
// opening a database or requiring a running PostgreSQL server.
func openProvider(
	ctx context.Context,
	cfg DatabaseConfig,
	openSQLite func(context.Context, string) (Provider, error),
	openPostgres func(context.Context, PostgresConfig) (Provider, error),
) (Provider, error) {
	switch cfg.Type {
	case databaseSQLite:
		return openSQLite(ctx, cfg.SQLite.Path)
	case databasePostgres:
		return openPostgres(ctx, cfg.Postgres)
	default:
		return nil, fmt.Errorf("balda.database.type must be resolved before opening state")
	}
}
