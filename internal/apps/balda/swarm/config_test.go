package swarm

import "testing"

func TestConfigNormalized_DefaultsModesToShadow(t *testing.T) {
	t.Parallel()

	got, err := (Config{Enabled: true}).Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v", err)
	}
	if got.Mode != ModeShadow {
		t.Fatalf("Mode = %q, want %q", got.Mode, ModeShadow)
	}
	if got.WebhookMode != ModeShadow {
		t.Fatalf("WebhookMode = %q, want %q", got.WebhookMode, ModeShadow)
	}
	if got.SchedulerMode != ModeShadow {
		t.Fatalf("SchedulerMode = %q, want %q", got.SchedulerMode, ModeShadow)
	}
}

func TestConfigNormalized_RejectsInvalidModes(t *testing.T) {
	t.Parallel()

	for _, cfg := range []Config{
		{Mode: "invalid"},
		{WebhookMode: "invalid"},
		{SchedulerMode: "invalid"},
	} {
		if _, err := cfg.Normalized(); err == nil {
			t.Fatalf("Normalized(%+v) error = nil, want non-nil", cfg)
		}
	}
}

func TestConfigMailboxEnabled_WhenAnySourceUsesMailbox(t *testing.T) {
	t.Parallel()

	for _, cfg := range []Config{
		{Enabled: true, Mode: ModeMailbox},
		{Enabled: true, WebhookMode: ModeMailbox},
		{Enabled: true, SchedulerMode: ModeMailbox},
	} {
		if !cfg.MailboxEnabled() {
			t.Fatalf("MailboxEnabled(%+v) = false, want true", cfg)
		}
	}
	if (Config{Enabled: false, Mode: ModeMailbox}).MailboxEnabled() {
		t.Fatal("MailboxEnabled(disabled) = true, want false")
	}
}
