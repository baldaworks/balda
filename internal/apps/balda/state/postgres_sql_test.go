package state

import "testing"

func TestPostgresBind(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ query, want string }{
		{"SELECT ? WHERE key = ?", "SELECT $1 WHERE key = $2"},
		{"SELECT '?' WHERE key = ?", "SELECT '?' WHERE key = $1"},
		{"SELECT 'it''s ?' WHERE key = ?", "SELECT 'it''s ?' WHERE key = $1"},
		{`SELECT "?" WHERE key = ?`, `SELECT "?" WHERE key = $1`},
	} {
		if got := postgresBind(tt.query); got != tt.want {
			t.Errorf("postgresBind(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}
