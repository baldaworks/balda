package slackagent

import (
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
)

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
