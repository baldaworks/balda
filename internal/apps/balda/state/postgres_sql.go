package state

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

const (
	postgresTimezone         = "UTC"
	postgresAfterClause      = " AND timestamp >= ?"
	postgresIngressPublished = "published"
)

func postgresBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

// postgresBind numbers placeholders after static SQL fragments are joined.
// Queries are adapter-owned SQL; user values always remain bound arguments.
func postgresBind(query string) string {
	var out strings.Builder
	index := 1
	var quote byte
	for i := 0; i < len(query); i++ {
		c := query[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
					out.WriteByte(quote)
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			out.WriteByte(c)
		case '?':
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(index))
			index++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func postgresErrorf(format string, args ...any) error {
	for i, arg := range args {
		if err, ok := arg.(error); ok {
			args[i] = redactPostgresError(err)
		}
	}
	return fmt.Errorf(format, args...)
}

func beginPostgresTx(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return nil, redactPostgresError(err)
	}
	// Existing state operations rely on SQLite's serialized transactions for
	// read/modify/write, event ordering and outbox sequence allocation. Keep
	// that guarantee across PostgreSQL connections and processes. The lock is
	// transaction-scoped and automatically released on rollback/disconnect.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1650551908, 1937006964)"); err != nil {
		_ = tx.Rollback()
		return nil, redactPostgresError(err)
	}
	return tx, nil
}
