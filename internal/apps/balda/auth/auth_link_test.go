package auth

import "testing"

func TestBuildOwnerAuthCommand(t *testing.T) {
	got := BuildOwnerAuthCommand(" owner-token ")
	want := "/start owner=owner-token"
	if got != want {
		t.Fatalf("BuildOwnerAuthCommand() = %q, want %q", got, want)
	}
}

func TestBuildOwnerAuthLink(t *testing.T) {
	t.Run("with username", func(t *testing.T) {
		got := BuildOwnerAuthLink("BaldaBot", "token123")
		want := "https://t.me/BaldaBot?start=owner_token123"
		if got != want {
			t.Fatalf("BuildOwnerAuthLink() = %q, want %q", got, want)
		}
	})

	t.Run("fallback username placeholder", func(t *testing.T) {
		got := BuildOwnerAuthLink(" ", "token123")
		want := "https://t.me/<bot_username>?start=owner_token123"
		if got != want {
			t.Fatalf("BuildOwnerAuthLink() = %q, want %q", got, want)
		}
	})
}
