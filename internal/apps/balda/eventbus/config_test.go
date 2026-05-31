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
	if !cfg.JetStream {
		t.Fatal("JetStream = false, want built-in runtime enabled")
	}
	if cfg.StoreDir != ".balda/nats" {
		t.Fatalf("StoreDir = %q, want .balda/nats", cfg.StoreDir)
	}
}

func TestConfigNormalized_ForcesBuiltInRuntimeOn(t *testing.T) {
	cfg, err := (Config{Embedded: true, JetStream: false}).Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v", err)
	}
	if !cfg.JetStream {
		t.Fatal("JetStream = false, want forced built-in runtime")
	}
}
