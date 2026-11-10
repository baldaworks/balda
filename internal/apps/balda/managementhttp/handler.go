package managementhttp

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const compatibilityPath = "/api/management/v1/compatibility"

// Services are the Core-owned capabilities exposed through management v1.
type Services struct {
	BrowserAuth *browserauth.Service
	Providers   *providers.Service
	MCP         mcpOperations
	Schedules   scheduleOperations
	Webhooks    webhookOperations
	Aliases     aliasOperations
	Hooks       hookOperations
	Plugins     pluginOperations
	Skills      skillOperations
}

type skillOperations interface {
	Inventory(ctx context.Context, authority skillcmd.Authority) (skillcmd.Inventory, error)
	ReviewInstall(ctx context.Context, request skillcmd.InstallReview) (skillcmd.Candidate, error)
	ReviewUpdate(ctx context.Context, request skillcmd.Mutation) (skillcmd.Candidate, error)
	Accept(ctx context.Context, handle string, authority skillcmd.Authority) (skillcmd.Outcome, error)
	Change(ctx context.Context, operation skillcmd.Operation, request skillcmd.Mutation) (skillcmd.Outcome, error)
	Refresh(ctx context.Context, request skillcmd.Mutation) (skillcmd.Outcome, error)
	RetryPublication(ctx context.Context, authority skillcmd.Authority) (skillcmd.Outcome, error)
}

type pluginOperations interface {
	Inventory(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.InstalledView, error)
	Detail(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.PluginDetail, error)
	Marketplaces(ctx context.Context, authority plugincmd.Authority) ([]plugincmd.MarketplaceView, error)
	Marketplace(ctx context.Context, name string, authority plugincmd.Authority) (plugincmd.MarketplaceDetail, error)
	AddMarketplace(ctx context.Context, request plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error)
	RefreshMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error)
	RemoveMarketplace(ctx context.Context, request plugincmd.MarketplaceMutation) error
	Candidate(ctx context.Context, name, marketplace string, authority plugincmd.Authority) (plugincmd.CandidateView, error)
	MutatePlugin(ctx context.Context, request plugincmd.PluginMutation) (plugincmd.MutationOutcome, error)
}

type hookOperations interface {
	CapabilityDescription() string
	Inventory(ctx context.Context, authority hookcmd.Authority) (hookcmd.Record, error)
	Save(ctx context.Context, request hookcmd.Save) error
	Delete(ctx context.Context, request hookcmd.Delete) error
}

type aliasOperations interface {
	List(ctx context.Context, authority aliascmd.Authority) ([]aliascmd.Record, error)
	Get(ctx context.Context, name string, authority aliascmd.Authority) (aliascmd.Record, error)
	Create(ctx context.Context, request aliascmd.Create) (aliascmd.Record, error)
	Retarget(ctx context.Context, request aliascmd.Retarget) (aliascmd.Record, error)
	Delete(ctx context.Context, request aliascmd.Delete) error
}

type webhookOperations interface {
	Inventory(ctx context.Context, authority webhookroutecmd.Authority) ([]webhookroutecmd.Item, error)
	Get(ctx context.Context, name string, authority webhookroutecmd.Authority) (webhookroutecmd.Item, error)
	Create(ctx context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error)
	Update(ctx context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error)
	SetEnabled(ctx context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error)
	Delete(ctx context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error)
	Rotate(ctx context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error)
	TestPost(ctx context.Context, request webhookroutecmd.TestPost) (webhookroutecmd.TestResult, error)
	History(ctx context.Context, name string, beforeAt time.Time, beforeJobID string, limit int, authority webhookroutecmd.Authority) ([]webhookroutecmd.HistoryItem, error)
	HistoryDetail(ctx context.Context, name, jobID string, authority webhookroutecmd.Authority) (webhookroutecmd.HistoryItem, error)
}

type scheduleOperations interface {
	Inventory(ctx context.Context, authority schedulecmd.Authority) ([]schedulecmd.Item, error)
	Get(ctx context.Context, id string, authority schedulecmd.Authority) (schedulecmd.Item, error)
	Create(ctx context.Context, request schedulecmd.Create) (schedulecmd.Item, error)
	Update(ctx context.Context, request schedulecmd.Update) (schedulecmd.Item, error)
	SetEnabled(ctx context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error)
	Delete(ctx context.Context, request schedulecmd.Delete) (schedulecmd.Item, error)
	RunNow(ctx context.Context, request schedulecmd.RunNow) (schedulecmd.RunItem, error)
	History(ctx context.Context, id string, beforeAt time.Time, beforeID string, limit int, authority schedulecmd.Authority) ([]schedulecmd.RunItem, error)
	RunDetail(ctx context.Context, scheduleID, runID string, authority schedulecmd.Authority) (schedulecmd.RunDetail, error)
}

type mcpOperations interface {
	Inventory(ctx context.Context) ([]mcpcmd.Item, error)
	ProviderIDs(ctx context.Context) ([]string, error)
	Create(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	Update(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
	SetEnabled(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Delete(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Probe(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	ProbeUpdate(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
	DiagnoseCurrent(ctx context.Context, id string, authority mcpcmd.Authority) (mcpcmd.DiagnosticResult, error)
	CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, authorization mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error)
	CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, authorization mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error)
	BeginBrowser(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error)
	BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error)
	CompleteBrowser(ctx context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Item, error)
	Device(ctx context.Context, attemptID string, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error)
	CurrentAttempt(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error)
	Cancel(ctx context.Context, attemptID string, authority mcpcmd.Authority) error
	Disconnect(ctx context.Context, connectionID string, authority mcpcmd.Authority) error
	RetryAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error)
}

// Handler returns the authenticated management API handler.
func (c ResolvedConfig) Handler(services Services) http.Handler {
	server := strictServer{browserAuth: services.BrowserAuth, providers: services.Providers, mcp: services.MCP, schedules: services.Schedules, webhooks: services.Webhooks, aliases: services.Aliases, hooks: services.Hooks, plugins: services.Plugins, skills: services.Skills}
	strict := managementv1.NewStrictHandlerWithOptions(server, nil, managementv1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeServiceError(w, r, err)
		},
	})
	mux := http.NewServeMux()
	generated := managementv1.HandlerFromMux(strict, mux)
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule, ok := managementRoute(r.Method, r.URL.Path)
		if !ok || (services.BrowserAuth == nil && r.URL.Path != compatibilityPath) ||
			(services.Schedules == nil && strings.HasPrefix(r.URL.Path, "/api/management/v1/schedules")) ||
			(services.Webhooks == nil && strings.HasPrefix(r.URL.Path, "/api/management/v1/webhooks")) ||
			(services.Aliases == nil && strings.HasPrefix(r.URL.Path, "/api/management/v1/aliases")) ||
			(services.Hooks == nil && strings.HasPrefix(r.URL.Path, "/api/management/v1/hooks")) {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		if services.Plugins == nil && (strings.HasPrefix(r.URL.Path, "/api/management/v1/plugins") || strings.HasPrefix(r.URL.Path, "/api/management/v1/plugin-marketplaces")) {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		if services.Skills == nil && strings.HasPrefix(r.URL.Path, "/api/management/v1/skills") {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		server.guard(rule, generated).ServeHTTP(w, r)
	})
	return withRequestID(c.auth.middleware(gated))
}
