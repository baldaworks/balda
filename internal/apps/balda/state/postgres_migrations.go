package state

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed postgres_migrations/*.sql
var postgresMigrationsFS embed.FS

func migratePostgres(ctx context.Context, db *sql.DB) error {
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		return fmt.Errorf("open postgres migration files: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create postgres migration lock: %w", err)
	}
	// SQLite Go migrations are globally registered. They must never be
	// included in PostgreSQL's independent migration history.
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithDisableGlobalRegistry(true), goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create postgres migration provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		var partial *goose.PartialError
		if errors.As(err, &partial) && partial.Failed != nil {
			return fmt.Errorf("apply postgres state migration %d: %w", partial.Failed.Source.Version, redactPostgresError(err))
		}
		return fmt.Errorf("apply postgres state migrations: %w", redactPostgresError(err))
	}
	for _, table := range requiredBaldaStateTables {
		var exists bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1
		)`, table).Scan(&exists)
		if err != nil {
			return fmt.Errorf("validate postgres state schema: %w", redactPostgresError(err))
		}
		if !exists {
			return fmt.Errorf("postgres state schema missing table %s", table)
		}
	}
	return nil
}
