package slackagent

import (
	"net/url"
	"slices"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
)

func TestSupportedCommandsAdvertiseSessionControls(t *testing.T) {
	commands := SupportedCommands()
	for _, want := range []string{"model", "reset", "new"} {
		if !slices.Contains(commands, want) {
			t.Fatalf("SupportedCommands() = %v, want %s", commands, want)
		}
	}
	commands[0] = "mutated"
	if slices.Contains(SupportedCommands(), "mutated") {
		t.Fatal("SupportedCommands returned mutable registry storage")
	}
}

func TestDecodeCommandRequestPublishesFreshLifetimeAliasesAndArguments(t *testing.T) {
	for _, name := range []string{"reset", "new"} {
		t.Run(name, func(t *testing.T) {
			form := url.Values{
				"command": {"/balda"}, "team_id": {"T1"}, "channel_id": {"C1"}, "user_id": {"U1"},
				"text": {name + " forbidden-selector"},
			}
			request, err := decodeCommandRequestWithRegistry([]byte(form.Encode()), nil)
			if err != nil {
				t.Fatal(err)
			}
			if request.Payload.Name != name || request.Payload.Args != "forbidden-selector" {
				t.Fatalf("payload = %#v", request.Payload)
			}
		})
	}
}

func TestDecodeCommandRequestAcceptsProjectedPluginAlias(t *testing.T) {
	t.Parallel()
	registry := commandcmd.NewRegistry()
	registry.Replace(ChannelType, commandcmd.AdvertisementProjection{Commands: []commandcmd.ProjectedCommand{{Name: "release"}}})
	form := url.Values{
		"command": {"/balda"}, "team_id": {"T1"}, "channel_id": {"C1"}, "user_id": {"U1"}, "text": {"release production"},
	}
	request, err := decodeCommandRequestWithRegistry([]byte(form.Encode()), registry)
	if err != nil {
		t.Fatalf("decodeCommandRequestWithRegistry() error = %v", err)
	}
	if request.Payload.Name != "release" || request.Payload.Args != "production" {
		t.Fatalf("payload = %#v", request.Payload)
	}
}
