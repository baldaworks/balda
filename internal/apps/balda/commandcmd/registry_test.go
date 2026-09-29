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

func TestRegistrySupportsAdvertisedBuiltinCommands(t *testing.T) {
	t.Parallel()
	registry := NewRegistryWithAdvertisements([]Advertisement{{Transport: "mattermost", Enabled: true, Names: []string{"locator"}}})
	if !registry.Supports("mattermost", "LOCATOR") {
		t.Fatal("Supports() = false, want advertised builtin command")
	}
	if registry.Supports("mattermost", "unknown") {
		t.Fatal("Supports() = true for an unadvertised command")
	}
}
