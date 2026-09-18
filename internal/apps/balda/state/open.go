package state

import (
	"context"
	"fmt"
)

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
