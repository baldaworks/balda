package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

func openPostgresDB(cfg PostgresConfig) (*sql.DB, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	u := url.URL{
		Scheme:   "postgresql",
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     "/" + cfg.Name,
		User:     url.UserPassword(cfg.User, cfg.Password),
		RawQuery: url.Values{"sslmode": {cfg.SSLMode}, "connect_timeout": {"10"}}.Encode(),
	}
	parsed, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("parse postgres state configuration: %w", redactPostgresError(err))
	}
	// Explicit settings take precedence even when empty, avoiding credentials
	// implicitly inherited from PG* variables or a local password file.
	parsed.Password = cfg.Password
	parsed.RuntimeParams["timezone"] = postgresTimezone
	db := stdlib.OpenDB(*parsed)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

type postgresStateError struct {
	cause error
	code  string
}

func (e *postgresStateError) Error() string {
	if e.code != "" {
		return "postgres state operation failed (SQLSTATE " + e.code + ")"
	}
	return "postgres state operation failed"
}

func (e *postgresStateError) Unwrap() error { return e.cause }

func redactPostgresError(err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) || errors.Is(err, sql.ErrTxDone) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var alreadyRedacted *postgresStateError
	if errors.As(err, &alreadyRedacted) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &postgresStateError{cause: err, code: pgErr.Code}
	}
	return &postgresStateError{cause: err}
}
