package balda

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	resetcommand "github.com/baldaworks/balda/internal/apps/balda/actors/command/reset"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/lifetimeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	providerapp "github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

// This extends the ordinary browser fixture with a real command/lifetime/native
// boundary. It does not replace protocol, archive, provider or catalog owners.
func verifyOAuthBrowserFreshLifetime(t *testing.T, catalog *catalogapp.Runtime, registry *mcpregistry.MapRegistry, binder baldaagent.SessionCapabilityBinder, configs map[string]agentconfig.Config, p state.Provider, credentials *mcpmanage.Service, store *oauthControlledExpiryStore, issuer *backofficeOAuthIssuer, stateDir, baseURL, alias string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	rpcLog := filepath.Join(workspace, "native-rpc.log")
	config := configs["managed-acp"]
	acpConfig := *config.GenericACP
	acpConfig.Model, acpConfig.ReasoningEffort = "fixture-model", "high"
	acpConfig.Env = map[string]string{"BALDA_OAUTH_LIFETIME_RPC_LOG": rpcLog}
	config.GenericACP = &acpConfig
	configs["managed-acp"] = config
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	skillPath := filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md")
	write(skillPath, "---\nname: review\ndescription: Review A.\n---\nReview A instructions.\n")
	bundled := mcp.NewServer(&mcp.Implementation{Name: "lifetime-fixture", Version: "1"}, nil)
	bundledHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return bundled }, nil))
	defer bundledHTTP.Close()
	registry.Set("balda", agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: bundledHTTP.URL})
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{Factory: agentfactory.New(configs, registry), ScopedFactory: catalogapp.NewProviderFactory(configs, registry), NormaCfg: runtimeconfig.RuntimeConfig{Providers: configs}, SessionService: p.RuntimeSessions()})
	runtimes := baldaagent.NewRuntimeManager(baldaagent.RuntimeManagerParams{Builder: builder, BaldaProviderID: "managed-acp", WorkingDir: workspace, StateDir: stateDir, CapabilityBinder: binder, MCPRegistry: registry, ProviderService: providerapp.New(configs, p.ProviderManagement(), nil), Logger: zerolog.Nop()})
	defer func() { _ = runtimes.Stop(context.Background()) }()
	manager, err := baldasession.NewManager(baldasession.ManagerParams{AgentBuilder: sessionapp.SessionAgentBuilderAdapter{Builder: builder}, RuntimeManager: sessionapp.SessionRuntimeManagerAdapter{Manager: runtimes}, Archive: p.ChatArchive(), SessionStore: p.Sessions(), RuntimeOperations: p.RuntimeOperations(), SessionsPersistent: true, WorkingDir: workspace, BaldaProviderID: "managed-acp", Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	locator := archiveIntegrationLocator()
	topic, err := manager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: "fixture-user", Kind: baldasession.KindChat}, "saved-label")
	if err != nil {
		t.Fatal(err)
	}
	queue := actors.NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = queue.Shutdown(context.Background()) }()
	old, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	invoke := func(lease *baldasession.RuntimeLease) (string, error) {
		var output strings.Builder
		for event, runErr := range lease.Runner().Run(ctx, "fixture-user", lease.SessionID(), genai.NewContentFromText("invoke worker", genai.RoleUser), adkagent.RunConfig{}) {
			if runErr != nil {
				return output.String(), runErr
			}
			if event.ErrorCode != "" || event.ErrorMessage != "" {
				return output.String(), fmt.Errorf("native invocation event failed")
			}
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					output.WriteString(part.Text)
				}
			}
		}
		return output.String(), nil
	}
	first, err := invoke(old)
	if err != nil || !strings.Contains(first, "actual ACP tool /mcp") || issuer.acpCalls.Load() != 1 {
		t.Fatalf("historical A tool invocation: err=%v calls=%d", err, issuer.acpCalls.Load())
	}
	revisionA := oauthBrowserConnectionRevision(t, p, "managed-tools")
	bindingA := *revisionA.Definition.AuthBinding
	grantA, found, err := store.GrantStore.GetMCPGrant(ctx, bindingA)
	if err != nil || !found {
		t.Fatal("historical grant unavailable")
	}
	store.expiredGeneration.Store(grantA.Generation)
	issuer.currentAccess.Store("")
	issuer.rejectRefresh.Store(true)
	upstream := issuer.upstreamRequests.Load()
	_, expiredErr := invoke(old)
	if expiredErr == nil || issuer.upstreamRequests.Load() != upstream {
		t.Fatal("expired A did not fail before upstream resource traffic")
	}
	deniedA, found, err := store.GrantStore.GetMCPGrant(ctx, bindingA)
	if err != nil || !found || deniedA.Status != mcpcmd.GrantAuthRequired {
		t.Fatalf("A did not become authorization-required: status=%s refresh_calls=%d", deniedA.Status, issuer.refreshes.Load())
	}
	issuer.rejectRefresh.Store(false)
	store.expiredGeneration.Store(0)
	runOAuthBrowser(t, baseURL, issuer.server.URL, "browser", "managed-lifetime-restore", 1440)
	revisionB := oauthBrowserConnectionRevision(t, p, "managed-tools")
	if revisionB.ID == revisionA.ID || revisionB.Definition.AuthBinding == nil || *revisionB.Definition.AuthBinding == bindingA {
		t.Fatal("browser recovery did not select a distinct client-bound revision B")
	}
	grantB, found, err := store.GrantStore.GetMCPGrant(ctx, *revisionB.Definition.AuthBinding)
	if err != nil || !found || grantB.Status != mcpcmd.GrantAuthorized {
		t.Fatal("B is not authorized")
	}
	tokens, err := credentials.OpenGrant(grantB)
	if err != nil || tokens.RefreshToken == "" {
		t.Fatal("B browser consent did not grant renewable credentials")
	}
	upstream = issuer.upstreamRequests.Load()
	if _, err := invoke(old); err == nil || issuer.upstreamRequests.Load() != upstream {
		t.Fatal("ordinary A borrowed B authorization")
	}
	before, found, err := p.Sessions().GetBySessionID(ctx, locator.SessionID)
	if err != nil || !found {
		t.Fatal("source session metadata unavailable")
	}
	write(skillPath, "---\nname: review\ndescription: Review B.\n---\nReview B instructions.\n")
	pluginRoot := filepath.Join(stateDir, "plugins", "lifetime-tools")
	write(filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"lifetime-tools","version":"1.0.0","extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"freshtools","description":"Current tools","instruction":"Use current tools."}]}}}`)
	write(filepath.Join(pluginRoot, "skills", "current-tools", "SKILL.md"), "---\nname: current-tools\ndescription: Current plugin tools.\n---\nCurrent tools instructions.\n")
	plugins, err := pluginapp.NewManaged(stateDir, p.AppKV(), p.Plugins(), catalog, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.MigrateLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	expected, err := catalog.CurrentSkillSnapshot(ctx, workspace)
	if err != nil || string(expected.ID) == old.RuntimeSnapshotID() {
		t.Fatal("current catalog did not change")
	}
	presentation := deliveryfmt.NewStructuredRegistry()
	if err := deliveryfmt.RegisterStructuredRenderer(presentation, deliveryfmt.TransportTelegram, sessionfmt.NoticeDescriptor, oauthLifetimeNotice{}); err != nil {
		t.Fatal(err)
	}
	responses := &archiveIntegrationBus{}
	handler := resetcommand.New(lifetimeapp.New(queue, manager), presentation, responses)
	router, err := commandactor.NewRouter([]commandactor.Handler{handler, commandactor.Alias("new", handler)})
	if err != nil {
		t.Fatal(err)
	}
	actor := commandactor.NewActor(router, catalog, nil)
	envelope, err := commandcmd.NewEnvelope(commandcmd.Payload{Version: commandcmd.SchemaVersion, Name: alias, SnapshotID: runtimecatalogcmd.SnapshotID(old.RuntimeSnapshotID()), Locator: locator, Transport: "telegram", Principal: "fixture-user", Access: commandcmd.Access{SessionCommands: true}}, commandcmd.EnvelopeOptions{ID: "fresh-browser-" + alias, From: actorlayer.ActorAddress{Target: "fixture", Key: "browser"}})
	if err != nil {
		t.Fatal(err)
	}
	rpcBefore, err := os.ReadFile(rpcLog)
	if err != nil {
		t.Fatal(err)
	}
	upstream = issuer.upstreamRequests.Load()
	if err := actor.Handle(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	rpcAfter, err := os.ReadFile(rpcLog)
	if err != nil || string(rpcAfter) != string(rpcBefore) || issuer.upstreamRequests.Load() != upstream {
		t.Fatal("reset performed provider/MCP RPC before an ordinary turn")
	}
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	after, found, err := p.Sessions().GetBySessionID(ctx, locator.SessionID)
	if err != nil || !found || current.RuntimeSnapshotID() != string(expected.ID) || current.Generation() != old.Generation()+1 || current.ArchiveID() == old.ArchiveID() || current.Session().Events().Len() != 0 {
		t.Fatal("command did not publish a new empty lifetime on catalog B")
	}
	if after.SessionID != before.SessionID || after.AddressKey != before.AddressKey || after.WorkspaceDir != before.WorkspaceDir || after.BranchName != before.BranchName || after.ProviderBindingJSON != before.ProviderBindingJSON || !reflect.DeepEqual(after.RequestedModel, before.RequestedModel) || after.EffectiveModel != before.EffectiveModel || current.PublicRuntimeMetadata().ReasoningEffort != old.PublicRuntimeMetadata().ReasoningEffort {
		t.Fatalf("catalog identity mismatch: session=%v address=%v workspace=%v branch=%v binding=%v preference=%v model=%v reasoning=%v", after.SessionID != before.SessionID, after.AddressKey != before.AddressKey, after.WorkspaceDir != before.WorkspaceDir, after.BranchName != before.BranchName, after.ProviderBindingJSON != before.ProviderBindingJSON, !reflect.DeepEqual(after.RequestedModel, before.RequestedModel), after.EffectiveModel != before.EffectiveModel, current.PublicRuntimeMetadata().ReasoningEffort != old.PublicRuntimeMetadata().ReasoningEffort)
	}
	if _, known := current.ModelObservation(); known {
		t.Fatal("reset invented active model evidence")
	}
	pluginCommand, pluginSkill, workspaceSkill := false, false, false
	for _, c := range expected.Commands {
		pluginCommand = pluginCommand || c.Name == "freshtools"
	}
	for _, s := range expected.Skills {
		pluginSkill = pluginSkill || s.Name == "current-tools"
		workspaceSkill = workspaceSkill || s.Name == "review" && s.Description == "Review B."
	}
	if !pluginCommand || !pluginSkill || !workspaceSkill {
		t.Fatalf("replacement catalog contributions: plugin_command=%v plugin_skill=%v workspace_skill=%v", pluginCommand, pluginSkill, workspaceSkill)
	}
	second, err := invoke(current)
	if err != nil || !strings.Contains(second, "actual ACP tool /mcp") || issuer.acpCalls.Load() != 2 {
		t.Fatalf("replacement B actual native initialize/list/call: %v", err)
	}
	stillA, found, err := store.GrantStore.GetMCPGrant(ctx, bindingA)
	retainedA, revisionFound, revisionErr := p.MCP().GetMCPRevision(ctx, revisionA.ConnectionID, revisionA.ID)
	if err != nil || !found || !reflect.DeepEqual(stillA, deniedA) || revisionErr != nil || !revisionFound || !sameOAuthBrowserRevision(retainedA, revisionA) || old.RuntimeSnapshotID() == current.RuntimeSnapshotID() {
		t.Fatal("replacement mutated historical A or reused its pin")
	}
	for _, output := range []string{first, second} {
		if strings.Contains(output, "synthetic-browser-") || strings.Contains(output, "synthetic-client-secret") {
			t.Fatal("native output disclosed upstream credentials")
		}
	}
	if err := actor.Handle(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	if responses.sends != 2 {
		t.Fatalf("command response/replay count=%d", responses.sends)
	}
}

type oauthLifetimeNotice struct{}

func (oauthLifetimeNotice) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[sessionfmt.Notice]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: fmt.Sprintf("New local conversation: %s", env.Body.SessionID), DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}, nil
}
