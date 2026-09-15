package commandfx

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
)

func TestRegistryAdvertisementTargetAppliesTransportSyntax(t *testing.T) {
	t.Parallel()
	registry := commandcmd.NewRegistry()
	telegram := NewRegistryAdvertisementTarget("telegram", registry)
	slack := NewRegistryAdvertisementTarget("slackagent", registry)
	if telegram.SupportsCommand("release-prod") {
		t.Fatal("telegram accepted a hyphenated command")
	}
	if !slack.SupportsCommand("release-prod") {
		t.Fatal("slackagent rejected a supported hyphenated command")
	}
	projection := commandcmd.AdvertisementProjection{Commands: []commandcmd.ProjectedCommand{{Name: "release"}}}
	if err := telegram.ReplaceCommands(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if !registry.Supports("telegram", "release") {
		t.Fatal("projected command is unavailable to transport parser")
	}
}
