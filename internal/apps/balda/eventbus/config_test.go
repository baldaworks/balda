package eventbus

import "testing"

func TestConfigNormalized_DefaultsToBuiltInRuntime(t *testing.T) {
	cfg, err := (Config{}).Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v", err)
	}
	if !cfg.Embedded {
		t.Fatal("Embedded = false, want true")
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != -1 {
		t.Fatalf("address = %s:%d, want 127.0.0.1:-1", cfg.Host, cfg.Port)
	}
	if cfg.StoreDir != ".balda/nats" {
		t.Fatalf("StoreDir = %q, want .balda/nats", cfg.StoreDir)
	}
}

func TestConfigNormalized_LeavesExternalRuntimeDisabledWhenURLsAreProvided(t *testing.T) {
	cfg, err := (Config{Embedded: false, URLs: []string{"nats://example:4222"}}).Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v, want nil", err)
	}
	if cfg.Embedded {
		t.Fatal("Embedded = true, want false when URLs are provided")
	}
	if got, want := cfg.URLs, []string{"nats://example:4222"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("URLs = %#v, want %#v", got, want)
	}
}
