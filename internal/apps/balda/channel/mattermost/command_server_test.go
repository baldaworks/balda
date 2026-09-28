package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// commandRecorder captures the commands the server forwards.
type commandRecorder struct {
	commands []InboundCommand
	unsup    []InboundCommand
	err      error
}

func (r *commandRecorder) ProcessInbound(context.Context, InboundMessage) (turncmd.InboundSettlement, error) {
	return turncmd.InboundSettlement{}, nil
}

func (r *commandRecorder) HandleCommand(_ context.Context, cmd InboundCommand) error {
	r.commands = append(r.commands, cmd)
	return r.err
}

func (r *commandRecorder) HandleUnsupportedCommand(_ context.Context, cmd InboundCommand) error {
	r.unsup = append(r.unsup, cmd)
	return nil
}

func newTestCommandServer(recorder *commandRecorder) *CommandServer {
	return NewCommandServer(CommandServerParams{
		Processor: recorder,
		Commands: commandcmd.NewRegistryWithAdvertisements([]commandcmd.Advertisement{{
			Transport: ChannelType,
			Enabled:   true,
			Names:     []string{"locator", "reset", "usage", "auto", "cancel", "goalkeeper", "topic", "close", "start", "user", "skill"},
		}}),
		Config: CommandServerConfig{Enabled: true, Path: "/mattermost/commands", Token: testCommandToken},
	})
}

func commandRequest(form url.Values) (*http.Request, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(http.MethodPost, "/mattermost/commands", strings.NewReader(form.Encode()))
	return request, httptest.NewRecorder()
}

// TestCommandServerRejectsMismatchedToken covers the primary credential
// boundary: a slash request whose token does not match the configured one must
// never reach the inbound processor.
func TestCommandServerRejectsMismatchedToken(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":      {"wrong-token"},
		"command":    {"/locator"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if len(recorder.commands) != 0 {
		t.Fatalf("commands = %d, want 0: a mismatched token must not publish", len(recorder.commands))
	}
}

// TestCommandServerRejectsEmptyConfiguredToken covers the fail-closed default:
// with no token configured nothing is authorised, so a blank config cannot leave
// the receiver open.
func TestCommandServerRejectsEmptyConfiguredToken(t *testing.T) {
	recorder := &commandRecorder{}
	server := NewCommandServer(CommandServerParams{
		Processor: recorder,
		Config:    CommandServerConfig{Enabled: true, Path: "/mattermost/commands", Token: ""},
	})
	request, response := commandRequest(url.Values{
		"token":      {""},
		"command":    {"/locator"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if len(recorder.commands) != 0 {
		t.Fatalf("commands = %d, want 0", len(recorder.commands))
	}
}

// TestCommandServerPublishesSlashCommand covers the happy path with the
// documented one-command-per-action integration shape: the command name is in
// "command" and the arguments are in "text".
func TestCommandServerPublishesSlashCommand(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":      {testCommandToken},
		"command":    {"/locator"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if len(recorder.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(recorder.commands))
	}
	command := recorder.commands[0]
	if command.Command != "locator" {
		t.Fatalf("command = %q, want locator", command.Command)
	}
	if command.SenderID != testUserID {
		t.Fatalf("sender = %q, want %s", command.SenderID, testUserID)
	}
	if command.Locator.ChannelType != string(deliverycmd.ChannelTypeMattermost) {
		t.Fatalf("channel type = %q, want mattermost", command.Locator.ChannelType)
	}
	if command.InvocationID == "" {
		t.Fatal("slash command must carry a body-derived invocation id")
	}
}

// TestCommandServerUsesDistinctInvocationIDsWithoutPostIDs covers the provider
// contract: slash commands execute without creating Mattermost posts. Their
// body-derived invocation IDs must therefore stay distinct, so one command
// cannot deduplicate a later command.
func TestCommandServerUsesDistinctInvocationIDsWithoutPostIDs(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)

	for _, commandName := range []string{"/locator", "/reset"} {
		request, response := commandRequest(url.Values{
			"token":      {testCommandToken},
			"command":    {commandName},
			"channel_id": {testChannelID},
			"user_id":    {testUserID},
		})
		server.handleCommand(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status for %s = %d, want %d", commandName, response.Code, http.StatusOK)
		}
	}

	if len(recorder.commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(recorder.commands))
	}
	if recorder.commands[0].InvocationID == recorder.commands[1].InvocationID {
		t.Fatalf("slash commands shared invocation id %q", recorder.commands[0].InvocationID)
	}
}

func TestCommandServerUsesDistinctInvocationIDsForRepeatedIdenticalRequests(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	form := url.Values{
		"token":      {testCommandToken},
		"command":    {"/reset"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	}
	for range 2 {
		request, response := commandRequest(form)
		server.handleCommand(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
	}
	if len(recorder.commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(recorder.commands))
	}
	if recorder.commands[0].InvocationID == recorder.commands[1].InvocationID {
		t.Fatalf("identical slash requests shared invocation id %q", recorder.commands[0].InvocationID)
	}
}

// TestCommandServerReadsRootCommandWithSubcommand covers the single-root-command
// integration shape, where the command name is the first word of "text".
func TestCommandServerReadsRootCommandWithSubcommand(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":      {testCommandToken},
		"command":    {"/balda"},
		"text":       {"skill writer"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if len(recorder.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(recorder.commands))
	}
	command := recorder.commands[0]
	if command.Command != "skill" {
		t.Fatalf("command = %q, want skill", command.Command)
	}
	if command.Args != "writer" {
		t.Fatalf("args = %q, want writer", command.Args)
	}
}

// TestCommandServerReadsSlashPrefixedText covers the shape where the whole
// invocation arrives in "text" with its own leading slash.
func TestCommandServerReadsSlashPrefixedText(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":      {testCommandToken},
		"command":    {"/balda"},
		"text":       {"/reset now"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if len(recorder.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(recorder.commands))
	}
	command := recorder.commands[0]
	if command.Command != "reset" {
		t.Fatalf("command = %q, want reset", command.Command)
	}
	if command.Args != "now" {
		t.Fatalf("args = %q, want now", command.Args)
	}
}

// TestCommandServerAnswersUnsupportedCommandAsUsage verifies an unknown
// subcommand of a root slash command produces a usage answer rather than silence
// or a published command, mirroring the Slack receiver.
func TestCommandServerAnswersUnsupportedCommandAsUsage(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":      {testCommandToken},
		"command":    {"/balda"},
		"text":       {"nonsense"},
		"channel_id": {testChannelID},
		"user_id":    {testUserID},
	})

	server.handleCommand(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if len(recorder.commands) != 0 {
		t.Fatalf("commands = %d, want 0", len(recorder.commands))
	}
	var payload commandResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Text == "" {
		t.Fatal("expected a usage answer for an unsupported command")
	}
}

// TestCommandServerRejectsNonPost verifies only POST is accepted.
func TestCommandServerRejectsNonPost(t *testing.T) {
	server := newTestCommandServer(&commandRecorder{})
	request := httptest.NewRequest(http.MethodGet, "/mattermost/commands", nil)
	response := httptest.NewRecorder()

	server.handleCommand(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

// TestCommandServerRequiresChannelAndUser verifies a request missing the
// addressing fields is refused.
func TestCommandServerRequiresChannelAndUser(t *testing.T) {
	recorder := &commandRecorder{}
	server := newTestCommandServer(recorder)
	request, response := commandRequest(url.Values{
		"token":   {testCommandToken},
		"command": {"/locator"},
		"user_id": {testUserID},
	})

	server.handleCommand(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if len(recorder.commands) != 0 {
		t.Fatalf("commands = %d, want 0", len(recorder.commands))
	}
}

// TestCommandServerRejectsOversizedBody verifies the body limit is enforced.
func TestCommandServerRejectsOversizedBody(t *testing.T) {
	server := newTestCommandServer(&commandRecorder{})
	body := strings.Repeat("a", commandServerMaxBodyBytes+1)
	request := httptest.NewRequest(http.MethodPost, "/mattermost/commands", strings.NewReader(body))
	response := httptest.NewRecorder()

	server.handleCommand(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestCommandServerStartRefusesWithoutToken verifies the receiver refuses to
// start when enabled but unauthenticated.
func TestCommandServerStartRefusesWithoutToken(t *testing.T) {
	server := NewCommandServer(CommandServerParams{
		Processor: &commandRecorder{},
		Config:    CommandServerConfig{Enabled: true, Path: "/mattermost/commands", Token: ""},
	})
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail without a configured slash-command token")
	}
}

// TestCommandServerStartRefusesInvalidPath verifies a path not starting with "/"
// is rejected.
func TestCommandServerStartRefusesInvalidPath(t *testing.T) {
	server := NewCommandServer(CommandServerParams{
		Processor: &commandRecorder{},
		Config:    CommandServerConfig{Enabled: true, Path: "mattermost/commands", Token: testCommandToken},
	})
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail for a path without a leading slash")
	}
}

// TestCommandServerDisabledDoesNotStart verifies a disabled receiver is a no-op.
func TestCommandServerDisabledDoesNotStart(t *testing.T) {
	server := NewCommandServer(CommandServerParams{Processor: &commandRecorder{}})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("disabled Start returned %v, want nil", err)
	}
	if server.server != nil {
		t.Fatal("disabled receiver must not bind a listener")
	}
}

// TestCommandServerInvocationIDIsStable covers the idempotency helper: the same
// body must produce the same id, and different bodies must differ.
func TestCommandServerInvocationIDIsStable(t *testing.T) {
	first := commandInvocationID([]byte("token=x&command=%2Flocator"))
	second := commandInvocationID([]byte("token=x&command=%2Flocator"))
	if first != second {
		t.Fatalf("invocation id is not stable: %q vs %q", first, second)
	}
	other := commandInvocationID([]byte("token=x&command=%2Freset"))
	if other == first {
		t.Fatal("different bodies produced the same invocation id")
	}
	if !strings.HasPrefix(first, "mattermost:command:") {
		t.Fatalf("invocation id %q is missing its transport prefix", first)
	}
}

// TestVerifyCommandToken covers the token comparison helper.
func TestVerifyCommandToken(t *testing.T) {
	if err := verifyCommandToken(testCommandToken, testCommandToken); err != nil {
		t.Fatalf("identical tokens must verify: %v", err)
	}
	if err := verifyCommandToken(testCommandToken, "wrong-token"); err == nil {
		t.Fatal("different tokens must not verify")
	}
	if err := verifyCommandToken(testCommandToken, testCommandToken+"-longer"); err == nil {
		t.Fatal("different lengths must not verify")
	}
	if err := verifyCommandToken("", testCommandToken); err == nil {
		t.Fatal("an empty configured token must never verify")
	}
}

// TestNormalizeCommandPath covers the default and the rejection case.
func TestNormalizeCommandPath(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default", input: "", want: "/mattermost/commands"},
		{name: "explicit", input: "/custom/commands", want: "/custom/commands"},
		{name: "missing slash", input: "commands", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeCommandPath(testCase.input)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", testCase.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCommandPath(%q): %v", testCase.input, err)
			}
			if got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}
