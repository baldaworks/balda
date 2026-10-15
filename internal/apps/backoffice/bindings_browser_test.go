package backoffice

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

func TestHTTPAppBindingBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	provider, config := newHTTPAppTestState(t)
	config.Balda.Telegram.Enabled, config.Balda.Slack.Agent.Enabled = true, true
	config.Balda.Zulip.Webhook.Enabled, config.Balda.Mattermost.Enabled = true, true
	now := time.Now().UTC()
	for _, id := range []string{"administrator", string(usercmd.RoleOperator), "secondary"} {
		role := usercmd.RoleAdministrator
		if id == string(usercmd.RoleOperator) {
			role = usercmd.RoleOperator
		}
		createAccessTestUser(t, provider.Users(), usercmd.User{ID: id, DisplayName: id, Username: id, NormalizedUsername: id, Status: usercmd.StatusActive, Role: role, Primary: id == "administrator", Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now})
	}
	invitations, err := auth.NewBindingInvitations(provider.Users().(usercmd.InvitationStore))
	if err != nil {
		t.Fatal(err)
	}
	channels := auth.NewBindingChannels([]string{"telegram", slackagent.ChannelType, "zulip", "mattermost"})
	for _, channel := range []string{"telegram", slackagent.ChannelType, "zulip", "mattermost"} {
		key := "verified-" + channel
		if channel == slackagent.ChannelType {
			key = "TQA:UBOT"
		}
		if err := channels.Register(usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: channel, Key: key}, Name: "Verified fixture", BotUsername: "fixture_bot", Endpoint: "https://chat.example.test", CommandsEnabled: channel != "mattermost"}); err != nil {
			t.Fatal(err)
		}
	}
	// The browser sends a real signed slash request to the concrete Slack receiver.
	// Other adapters' trusted native fixtures are covered in handlersfx; this private
	// loopback fixture exercises their already-normalized proof at the shared port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	slackAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	slack := slackagent.NewServer(nil, nil, nil, nil, slackagent.Config{Enabled: true, ListenAddr: slackAddress, SigningSecret: "browser-fixture-signing-secret"}, zerolog.Nop())
	slack.SetBindingAdmitter(&browserSlackAdmitter{invitations: invitations, channels: channels}, nil)
	if err := slack.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := slack.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewUnstartedServer(nil)
	config.Server.PublicURL = "http://" + server.Listener.Addr().String()
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	app.invitations, app.bindingChannels = invitations, channels
	handler, err := app.handler()
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	fixture := http.NewServeMux()
	fixture.Handle("/", handler)
	fixture.HandleFunc("POST /__fixture/consume", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Channel, Payload, Sender string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		info, ok := channels.Get(input.Channel)
		if !ok || input.Channel == slackagent.ChannelType {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		principal := input.Sender
		if input.Channel == "telegram" || input.Channel == "zulip" {
			value, err := strconv.Atoi(input.Sender)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			principal = strconv.Itoa(value + 1000)
		}
		_, err := invitations.Consume(r.Context(), usercmd.BindingProof{Payload: input.Payload, Integration: info.Integration, Principal: principal, DisplayName: "Verified fixture sender", Direct: true, Locator: deliverycmd.Locator{ChannelType: input.Channel, AddressKey: principal, SessionID: principal}})
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server.Config.Handler = fixture
	server.Start()
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/bindings.cjs", server.URL, "http://"+slackAddress)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("binding browser workflow: %v\n%s", err, output)
	}
	for _, id := range []string{string(usercmd.RoleOperator), "secondary"} {
		user, found, err := provider.Users().GetUser(t.Context(), id)
		if err != nil || !found || user.Primary || len(user.Bindings) != 8 {
			t.Fatalf("selected %s binding count/primary: %d/%t: %v", id, len(user.Bindings), user.Primary, err)
		}
		expectedRole := usercmd.RoleAdministrator
		if id == string(usercmd.RoleOperator) {
			expectedRole = usercmd.RoleOperator
		}
		if user.Role != expectedRole {
			t.Fatalf("%s role changed", id)
		}
	}
	t.Log(string(output))
}

type browserSlackAdmitter struct {
	invitations *auth.BindingInvitations
	channels    *auth.BindingChannels
}

func (a *browserSlackAdmitter) Consume(ctx context.Context, proof usercmd.BindingProof) (string, error) {
	info, _ := a.channels.Get(slackagent.ChannelType)
	subject := strings.Split(proof.Principal, ":")
	address, ok, err := slackagent.DecodeLocator(proof.Locator)
	if err != nil || !ok || address.TeamID != "TQA" || len(subject) != 3 || subject[0] != slackagent.ChannelType || subject[1] != "TQA" {
		return "", usercmd.ErrBindingInvitationScope
	}
	proof.Integration, proof.Principal = info.Integration, subject[1]+":"+subject[2]
	return a.invitations.Consume(ctx, proof)
}
