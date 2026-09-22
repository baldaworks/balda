package state

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"text/template/parse"
)

// DefaultSQLitePath keeps existing installations on their current database.
const DefaultSQLitePath = "{{.StateDir}}/state.db"

const (
	databaseSQLite   = "sqlite"
	databasePostgres = "postgres"
)

// DatabaseConfig selects the database used by all Balda state capabilities.
type DatabaseConfig struct {
	Type     string         `mapstructure:"type"`
	SQLite   SQLiteConfig   `mapstructure:"sqlite"`
	Postgres PostgresConfig `mapstructure:"postgres"`
}

// SQLiteConfig configures the SQLite file path template.
type SQLiteConfig struct {
	Path string `mapstructure:"path"`
}

// PostgresConfig contains structured PostgreSQL connection settings.
type PostgresConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Name     string `mapstructure:"name"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password" json:"-"`
	SSLMode  string `mapstructure:"sslmode"`
}

// String prevents accidental credential disclosure when formatting settings.
func (c PostgresConfig) String() string { return "postgres configuration (redacted)" }

// GoString also redacts Go-syntax formatting.
func (c PostgresConfig) GoString() string { return c.String() }

// Resolve validates the selected backend and renders its file path once.
// The returned configuration contains an absolute SQLite path, not a template.
func (c DatabaseConfig) Resolve(workingDir, stateDir string) (DatabaseConfig, error) {
	if c.Type == "" {
		c.Type = databaseSQLite
	}
	switch c.Type {
	case databaseSQLite:
		path, err := resolveDatabasePath(c.SQLite.Path, workingDir, stateDir)
		if err != nil {
			return DatabaseConfig{}, err
		}
		c.SQLite.Path = path
	case databasePostgres:
		if err := c.Postgres.validate(); err != nil {
			return DatabaseConfig{}, err
		}
	default:
		return DatabaseConfig{}, fmt.Errorf("balda.database.type must be sqlite or postgres")
	}
	return c, nil
}

func (c PostgresConfig) validate() error {
	for _, field := range []struct{ key, value string }{
		{"host", c.Host}, {"name", c.Name}, {"user", c.User},
	} {
		if strings.TrimSpace(field.value) == "" || strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("balda.database.postgres.%s is required and must not contain NUL", field.key)
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("balda.database.postgres.port must be between 1 and 65535")
	}
	switch c.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("balda.database.postgres.sslmode is invalid")
	}
	if strings.ContainsRune(c.Password, '\x00') {
		return fmt.Errorf("balda.database.postgres.password must not contain NUL")
	}
	return nil
}

func resolveDatabasePath(raw, workingDir, stateDir string) (string, error) {
	if !filepath.IsAbs(workingDir) || !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("database path resolution requires absolute working and state directories")
	}
	if raw == "" {
		raw = DefaultSQLitePath
	}
	tmpl, err := template.New("database-path").Option("missingkey=error").Parse(raw)
	if err != nil {
		return "", fmt.Errorf("balda.database.sqlite.path is not a valid path template")
	}
	// No function map alone is insufficient: text/template has built-in
	// functions. Allow only literal text and the single StateDir field.
	if len(tmpl.Templates()) != 1 || !validPathTemplate(tmpl.Root) {
		return "", fmt.Errorf("balda.database.sqlite.path permits only literal text and {{.StateDir}}")
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, struct{ StateDir string }{stateDir}); err != nil {
		return "", fmt.Errorf("render balda.database.sqlite.path failed")
	}
	path := out.String()
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("balda.database.sqlite.path must render a non-empty file path without NUL")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return filepath.Clean(path), nil
}

func validPathTemplate(root *parse.ListNode) bool {
	for _, node := range root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
		case *parse.ActionNode:
			if len(n.Pipe.Decl) != 0 || len(n.Pipe.Cmds) != 1 || len(n.Pipe.Cmds[0].Args) != 1 {
				return false
			}
			field, ok := n.Pipe.Cmds[0].Args[0].(*parse.FieldNode)
			if !ok || len(field.Ident) != 1 || field.Ident[0] != "StateDir" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
