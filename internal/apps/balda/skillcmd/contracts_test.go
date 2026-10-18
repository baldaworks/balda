package skillcmd

import (
	"testing"
	"time"
)

func TestAuthorityConstructorsAreClosed(t *testing.T) {
	now := time.Now().UTC()
	browser := NewBrowserAuthority("admin", 1, 2, 3, "session", 4, now)
	if !browser.Valid() || !browser.Browser() || browser.CLI() {
		t.Fatalf("browser authority is not valid: %+v", browser)
	}
	cli := NewCLIAuthority(now)
	if !cli.Valid() || !cli.CLI() || cli.Browser() {
		t.Fatalf("CLI authority is not valid: %+v", cli)
	}
	if (Authority{At: now}).Valid() {
		t.Fatal("a caller-constructed authority bypassed the closed kind")
	}
	cli.UserID = "forged-browser"
	if cli.Valid() {
		t.Fatal("CLI authority accepted browser identity material")
	}
}
