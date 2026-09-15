package commandcmd

import "testing"

func TestRegistryAtomicallyReplacesTransportCommands(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	registry.Replace("telegram", AdvertisementProjection{Commands: []ProjectedCommand{{Name: "release"}}})
	if !registry.Supports("telegram", "RELEASE") {
		t.Fatal("Supports() = false, want true")
	}
	registry.Replace("telegram", AdvertisementProjection{Commands: []ProjectedCommand{{Name: "deploy"}}})
	if registry.Supports("telegram", "release") || !registry.Supports("telegram", "deploy") {
		t.Fatal("Replace() did not atomically replace the command set")
	}
}
