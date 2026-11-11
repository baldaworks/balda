// Package browseredge owns the stateless standalone browser HTTP boundary.
package browseredge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/loginlimit"
	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const (
	accessCookie  = "balda_access"
	refreshCookie = "balda_refresh"
	csrfCookie    = "balda_csrf"
	mfaCookie     = "balda_webauthn"
	maxFormBytes  = 128 << 10
	htmlListEnd   = "</ul>"
)

// Service is the generated-client surface consumed by the browser edge.
type Service interface {
	Login(ctx context.Context, request managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error)
	FinishLogin(ctx context.Context, request managementv1.WebAuthLoginMFAFinishRequest) (managementv1.WebAuthCredentials, error)
	Principal(ctx context.Context, auth managementclient.Auth) (managementv1.WebAuthPrincipal, error)
	Refresh(ctx context.Context, auth managementclient.Auth) (managementv1.WebAuthCredentials, error)
	Logout(ctx context.Context, auth managementclient.Auth) error
	ReplacePassword(ctx context.Context, auth managementclient.Auth, request managementv1.WebAuthPasswordReplaceRequest) (managementv1.WebAuthCredentials, error)
	MFAStatus(ctx context.Context, auth managementclient.Auth) (managementv1.WebAuthMFAStatus, error)
	BeginMFA(ctx context.Context, auth managementclient.Auth, replace bool, password, binding *string) (managementv1.WebAuthMFAContinuation, error)
	FinishMFA(ctx context.Context, auth managementclient.Auth, replace bool, request managementv1.WebAuthMFAFinishRequest) (managementv1.WebAuthCredentials, error)
	DisableMFA(ctx context.Context, auth managementclient.Auth, password *string) (managementv1.WebAuthCredentials, error)
	Sessions(ctx context.Context, auth managementclient.Auth) (managementv1.WebAuthSessionPage, error)
	RevokeSession(ctx context.Context, auth managementclient.Auth, sessionID string, confirmCurrent bool) error
}

// SystemService is the optional generated-client configured system view.
type SystemService interface {
	SystemOverview(ctx context.Context, auth managementclient.Auth) (managementv1.SystemOverview, error)
	SystemAgent(ctx context.Context, auth managementclient.Auth) (managementv1.SystemAgent, error)
}

type MemoryService interface {
	MemoryFacts(ctx context.Context, auth managementclient.Auth, query string) (managementv1.MemoryFactInventory, error)
	MemoryFact(ctx context.Context, auth managementclient.Auth, id int64) (managementv1.MemoryFactEditor, error)
	CreateMemoryFact(ctx context.Context, auth managementclient.Auth, request managementv1.MemoryFactCreateRequest) (managementv1.MemoryFact, error)
	UpdateMemoryFact(ctx context.Context, auth managementclient.Auth, id int64, request managementv1.MemoryFactUpdateRequest) (managementv1.MemoryFact, error)
	DeleteMemoryFact(ctx context.Context, auth managementclient.Auth, id int64, request managementv1.MemoryFactDeleteRequest) error
}

type ArchiveService interface {
	AISessionArchives(ctx context.Context, auth managementclient.Auth, params *managementv1.ListAISessionArchivesParams) (managementv1.AISessionArchiveInventory, error)
	AISessionArchive(ctx context.Context, auth managementclient.Auth, id string, params *managementv1.GetAISessionArchiveParams) (managementv1.AISessionArchiveTranscript, error)
}

// UserService is the generated-client canonical-user administration surface.
type UserService interface {
	Users(ctx context.Context, auth managementclient.Auth) (managementv1.UserInventory, error)
	User(ctx context.Context, auth managementclient.Auth, userID string) (managementv1.UserRecord, error)
	UserSessions(ctx context.Context, auth managementclient.Auth, userID string) (managementv1.WebAuthSessionPage, error)
	CreateUser(ctx context.Context, auth managementclient.Auth, request managementv1.UserCreateRequest) (managementv1.UserRecord, error)
	UpdateUser(ctx context.Context, auth managementclient.Auth, userID string, request managementv1.UserUpdateRequest) (managementv1.UserRecord, error)
	ResetUserCredential(ctx context.Context, auth managementclient.Auth, userID string, request managementv1.UserCredentialResetRequest) error
	BindingChannels(ctx context.Context, auth managementclient.Auth) (managementv1.BindingChannelInventory, error)
	RefreshBindingChannel(ctx context.Context, auth managementclient.Auth, channel string) (managementv1.BindingChannel, error)
	UserBindings(ctx context.Context, auth managementclient.Auth, userID string) (managementv1.UserBindingInventory, error)
	DeleteUserBinding(ctx context.Context, auth managementclient.Auth, userID, bindingID string, request managementv1.BindingDeleteRequest) error
	IssueBindingInvitation(ctx context.Context, auth managementclient.Auth, userID string, request managementv1.BindingInvitationIssueRequest) (managementv1.IssuedBindingInvitation, error)
	CancelBindingInvitation(ctx context.Context, auth managementclient.Auth, userID, invitationID string, request managementv1.BindingInvitationCancelRequest) error
}

// AuditService is the generated-client read-only security audit surface.
type AuditService interface {
	AuditEvents(ctx context.Context, auth managementclient.Auth, params *managementv1.ListAuditEventsParams) (managementv1.AuditEventPage, error)
}

// ProviderService is the optional generated-client surface activated by the provider Story.
type ProviderService interface {
	Providers(ctx context.Context, auth managementclient.Auth) (managementv1.ProviderInventory, error)
	CreateProvider(ctx context.Context, auth managementclient.Auth, request managementv1.ProviderCreateRequest) error
	UpdateProvider(ctx context.Context, auth managementclient.Auth, providerID string, request managementv1.ProviderUpdateRequest) error
	SelectProvider(ctx context.Context, auth managementclient.Auth, providerID string, request managementv1.ProviderSelectionRequest) error
	DeleteProvider(ctx context.Context, auth managementclient.Auth, providerID string, request managementv1.ProviderDeleteRequest) error
}

type MCPService interface {
	MCPConnections(ctx context.Context, auth managementclient.Auth) (managementv1.MCPInventory, error)
	CreateMCPConnection(ctx context.Context, auth managementclient.Auth, request managementv1.MCPCreateRequest) (managementv1.MCPItem, error)
	ProbeMCPConnection(ctx context.Context, auth managementclient.Auth, request managementv1.MCPCreateRequest) (managementv1.MCPItem, error)
	UpdateMCPConnection(ctx context.Context, auth managementclient.Auth, id string, request managementv1.MCPUpdateRequest) (managementv1.MCPItem, error)
	ProbeMCPConnectionUpdate(ctx context.Context, auth managementclient.Auth, id string, request managementv1.MCPUpdateRequest) (managementv1.MCPItem, error)
	SelectMCPConnection(ctx context.Context, auth managementclient.Auth, id string, request managementv1.MCPSelectionRequest) (managementv1.MCPItem, error)
	DeleteMCPConnection(ctx context.Context, auth managementclient.Auth, id string, request managementv1.MCPSelectionRequest) (managementv1.MCPItem, error)
	DiagnoseMCPConnection(ctx context.Context, auth managementclient.Auth, id string) (managementv1.MCPDiagnostic, error)
	OnboardMCPBrowser(ctx context.Context, auth managementclient.Auth, request managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPBrowserOnboardResult, error)
	OnboardMCPDevice(ctx context.Context, auth managementclient.Auth, request managementv1.MCPAuthorizationOnboardRequest) (managementv1.MCPDeviceOnboardResult, error)
	BeginMCPBrowser(ctx context.Context, auth managementclient.Auth, id string, input managementv1.MCPAuthorizationInput) (managementv1.MCPBrowserAuthorizationStart, error)
	BeginMCPDevice(ctx context.Context, auth managementclient.Auth, id string, input managementv1.MCPAuthorizationInput) (managementv1.MCPDeviceAuthorizationStart, error)
	CompleteMCPBrowser(ctx context.Context, auth managementclient.Auth, callback managementv1.MCPBrowserCallback) (managementv1.MCPItem, error)
	MCPDeviceStatus(ctx context.Context, auth managementclient.Auth, attemptID string) (managementv1.MCPDeviceAuthorizationStatus, error)
	CancelMCPAuthorization(ctx context.Context, auth managementclient.Auth, attemptID string) error
	DisconnectMCPAuthorization(ctx context.Context, auth managementclient.Auth, id string) error
	RetryMCPAuthorization(ctx context.Context, auth managementclient.Auth, id string, request managementv1.MCPRetryAuthorizationRequest) (managementv1.MCPItem, error)
}

// ScheduleService is the optional generated-client schedule surface.
type ScheduleService interface {
	Schedules(ctx context.Context, auth managementclient.Auth) (managementv1.ScheduleInventory, error)
	Schedule(ctx context.Context, auth managementclient.Auth, id string) (managementv1.ScheduleItem, error)
	CreateSchedule(ctx context.Context, auth managementclient.Auth, request managementv1.ScheduleCreateRequest) (managementv1.ScheduleItem, error)
	UpdateSchedule(ctx context.Context, auth managementclient.Auth, id string, request managementv1.ScheduleUpdateRequest) (managementv1.ScheduleItem, error)
	SelectSchedule(ctx context.Context, auth managementclient.Auth, id string, request managementv1.ScheduleSelectionRequest) (managementv1.ScheduleItem, error)
	DeleteSchedule(ctx context.Context, auth managementclient.Auth, id string, request managementv1.ScheduleDeleteRequest) (managementv1.ScheduleItem, error)
	RunSchedule(ctx context.Context, auth managementclient.Auth, id string, request managementv1.ScheduleRunNowRequest) (managementv1.ScheduleRunItem, error)
	ScheduleRuns(ctx context.Context, auth managementclient.Auth, id string, params *managementv1.ListScheduleRunsParams) (managementv1.ScheduleRunPage, error)
	ScheduleRun(ctx context.Context, auth managementclient.Auth, scheduleID, runID string) (managementv1.ScheduleRunDetail, error)
}

type WebhookService interface {
	Webhooks(ctx context.Context, auth managementclient.Auth) (managementv1.WebhookInventory, error)
	Webhook(ctx context.Context, auth managementclient.Auth, name string) (managementv1.WebhookItem, error)
	CreateWebhook(ctx context.Context, auth managementclient.Auth, request managementv1.WebhookCreateRequest) (managementv1.WebhookSecretResult, error)
	UpdateWebhook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.WebhookUpdateRequest) (managementv1.WebhookItem, error)
	SelectWebhook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.WebhookSelectionRequest) (managementv1.WebhookItem, error)
	DeleteWebhook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.WebhookVersionRequest) (managementv1.WebhookItem, error)
	RotateWebhook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.WebhookVersionRequest) (managementv1.WebhookSecretResult, error)
	TestWebhook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.WebhookTestRequest) (managementv1.WebhookTestResult, error)
	WebhookHistory(ctx context.Context, auth managementclient.Auth, name string, params *managementv1.ListWebhookHistoryParams) (managementv1.WebhookHistoryPage, error)
	WebhookHistoryDetail(ctx context.Context, auth managementclient.Auth, name, jobID string) (managementv1.WebhookHistoryDetail, error)
}
type AliasService interface {
	Aliases(ctx context.Context, auth managementclient.Auth) (managementv1.AliasInventory, error)
	Alias(ctx context.Context, auth managementclient.Auth, name string) (managementv1.AliasRecord, error)
	CreateAlias(ctx context.Context, auth managementclient.Auth, request managementv1.AliasCreateRequest) (managementv1.AliasRecord, error)
	RetargetAlias(ctx context.Context, auth managementclient.Auth, name string, request managementv1.AliasRetargetRequest) (managementv1.AliasRecord, error)
	DeleteAlias(ctx context.Context, auth managementclient.Auth, name string, request managementv1.AliasDeleteRequest) error
}

// HookService is the generated-client managed-hook surface.
type HookService interface {
	Hooks(ctx context.Context, auth managementclient.Auth) (managementv1.HookInventory, error)
	CreateHook(ctx context.Context, auth managementclient.Auth, request managementv1.HookSaveRequest) error
	UpdateHook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.HookSaveRequest) error
	DeleteHook(ctx context.Context, auth managementclient.Auth, name string, request managementv1.HookDeleteRequest) error
}

// PluginService is the generated-client plugin lifecycle surface.
type PluginService interface {
	Plugins(ctx context.Context, auth managementclient.Auth) (managementv1.PluginInventory, error)
	Plugin(ctx context.Context, auth managementclient.Auth, name string) (managementv1.PluginDetail, error)
	ReviewPlugin(ctx context.Context, auth managementclient.Auth, name, marketplace string) (managementv1.PluginCandidate, error)
	MutatePlugin(ctx context.Context, auth managementclient.Auth, name string, request managementv1.PluginMutationRequest) (managementv1.PluginMutationOutcome, error)
	PluginMarketplaces(ctx context.Context, auth managementclient.Auth) (managementv1.PluginMarketplaceInventory, error)
	PluginMarketplace(ctx context.Context, auth managementclient.Auth, name string) (managementv1.PluginMarketplaceDetail, error)
	AddPluginMarketplace(ctx context.Context, auth managementclient.Auth, request managementv1.PluginMarketplaceAddRequest) (managementv1.PluginMarketplaceDetail, error)
	RefreshPluginMarketplace(ctx context.Context, auth managementclient.Auth, name string, request managementv1.PluginMarketplaceMutationRequest) (managementv1.PluginMarketplaceDetail, error)
	RemovePluginMarketplace(ctx context.Context, auth managementclient.Auth, name string, request managementv1.PluginMarketplaceMutationRequest) error
}

// SkillService is the generated-client standalone skill lifecycle surface.
type SkillService interface {
	Skills(ctx context.Context, auth managementclient.Auth) (managementv1.SkillInventory, error)
	ReviewSkillInstall(ctx context.Context, auth managementclient.Auth, request managementv1.SkillInstallReviewRequest) (managementv1.SkillCandidate, error)
	ReviewSkillUpdate(ctx context.Context, auth managementclient.Auth, id string, request managementv1.SkillObservedStateRequest) (managementv1.SkillCandidate, error)
	AcceptSkillReview(ctx context.Context, auth managementclient.Auth, request managementv1.SkillAcceptRequest) (managementv1.SkillMutationOutcome, error)
	MutateSkill(ctx context.Context, auth managementclient.Auth, id string, request managementv1.SkillMutationRequest) (managementv1.SkillMutationOutcome, error)
	RefreshSkills(ctx context.Context, auth managementclient.Auth, request managementv1.SkillObservedStateRequest) (managementv1.SkillMutationOutcome, error)
	RetrySkillPublication(ctx context.Context, auth managementclient.Auth) (managementv1.SkillMutationOutcome, error)
}

// Config fixes the browser trust boundary.
type Config struct {
	PublicOrigin     string
	MountPath        string
	WebhookURLPrefix string
}

// App is a stateless browser BFF.
type App struct {
	service          Service
	limiter          *loginlimit.Limiter
	origin           string
	mount            string
	secure           bool
	now              func() time.Time
	webhookURLPrefix string
}

const formYesValue = "yes"
const enabledHiddenInput = `<input type="hidden" name="enabled" value="yes">`
const nameInputLabel = `<label>Name <input name="name" maxlength="64" required></label>`
const (
	statusDisabled = "disabled"
	statusDeleted  = "deleted"
	statusEnabled  = "enabled"
)

// New validates and constructs the browser edge.
func New(service Service, config Config) (*App, error) {
	if service == nil {
		return nil, fmt.Errorf("management service is required")
	}
	origin, err := url.Parse(config.PublicOrigin)
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") || (origin.Path != "" && origin.Path != "/") {
		return nil, fmt.Errorf("public origin is invalid")
	}
	if config.MountPath == "" || config.MountPath[0] != '/' || strings.HasSuffix(config.MountPath, "/") {
		return nil, fmt.Errorf("mount path is invalid")
	}
	if config.WebhookURLPrefix == "" {
		config.WebhookURLPrefix = strings.TrimSuffix(config.PublicOrigin, "/") + "/webhooks/"
	}
	webhookPrefix, err := url.Parse(config.WebhookURLPrefix)
	if err != nil || webhookPrefix.Scheme != origin.Scheme || webhookPrefix.Host != origin.Host || webhookPrefix.RawQuery != "" ||
		webhookPrefix.Fragment != "" || !strings.HasSuffix(webhookPrefix.Path, "/webhooks/") {
		return nil, fmt.Errorf("webhook URL prefix is invalid")
	}
	return &App{service: service, limiter: loginlimit.New(), origin: strings.TrimSuffix(config.PublicOrigin, "/"),
		mount: config.MountPath, secure: origin.Scheme == "https", now: time.Now, webhookURLPrefix: config.WebhookURLPrefix}, nil
}

// Handler returns the complete authentication/account route set.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+a.path("/"), a.root)
	mux.HandleFunc("GET "+a.path("/login"), a.loginPage)
	mux.HandleFunc("POST "+a.path("/login"), a.login)
	mux.HandleFunc("POST "+a.path("/auth/webauthn/finish"), a.finishLogin)
	mux.HandleFunc("POST "+a.path("/auth/session/refresh"), a.refresh)
	mux.HandleFunc("POST "+a.path("/logout"), a.logout)
	mux.HandleFunc("GET "+a.path("/overview"), a.overview)
	mux.HandleFunc("GET "+a.path("/agent"), a.agentPage)
	mux.HandleFunc("GET "+a.path("/memory"), a.memoryPage)
	mux.HandleFunc("GET "+a.path("/memory/new"), a.newMemoryFactPage)
	mux.HandleFunc("POST "+a.path("/memory"), a.createMemoryFact)
	mux.HandleFunc("GET "+a.path("/memory/{fact_id}"), a.memoryFactPage)
	mux.HandleFunc("POST "+a.path("/memory/{fact_id}"), a.updateMemoryFact)
	mux.HandleFunc("POST "+a.path("/memory/{fact_id}/delete"), a.deleteMemoryFact)
	mux.HandleFunc("GET "+a.path("/chat-sessions"), a.archivePage)
	mux.HandleFunc("GET "+a.path("/chat-sessions/{ai_session_id}"), a.archiveTranscriptPage)
	mux.HandleFunc("GET "+a.path("/access/users"), a.usersPage)
	mux.HandleFunc("GET "+a.path("/access/users/new"), a.newUserPage)
	mux.HandleFunc("POST "+a.path("/access/users"), a.createUser)
	mux.HandleFunc("GET "+a.path("/access/users/{user_id}"), a.userPage)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}"), a.updateUser)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/credential-reset"), a.resetUserCredential)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/sessions/{session_id}/revoke"), a.revokeUserSession)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/bindings/{binding_id}/delete"), a.deleteUserBinding)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/binding-invitations"), a.issueBindingInvitation)
	mux.HandleFunc("POST "+a.path("/access/users/{user_id}/binding-invitations/{invitation_id}/cancel"), a.cancelBindingInvitation)
	mux.HandleFunc("POST "+a.path("/binding-channels/{channel_type}/refresh"), a.refreshBindingChannel)
	mux.HandleFunc("GET "+a.path("/audit"), a.auditPage)
	mux.HandleFunc("GET "+a.path("/account"), a.account)
	mux.HandleFunc("POST "+a.path("/account/password"), a.replacePassword)
	mux.HandleFunc("POST "+a.path("/account/2fa/enable/start"), a.beginMFA(false))
	mux.HandleFunc("POST "+a.path("/account/2fa/replace/start"), a.beginMFA(true))
	mux.HandleFunc("POST "+a.path("/account/2fa/enable/finish"), a.finishMFA(false))
	mux.HandleFunc("POST "+a.path("/account/2fa/replace/finish"), a.finishMFA(true))
	mux.HandleFunc("POST "+a.path("/account/2fa/disable"), a.disableMFA)
	mux.HandleFunc("POST "+a.path("/account/sessions/{session_id}/revoke"), a.revokeSession)
	mux.HandleFunc("GET "+a.path("/providers"), a.providersPage)
	mux.HandleFunc("POST "+a.path("/providers"), a.createProvider)
	mux.HandleFunc("GET "+a.path("/providers/new"), a.newProviderPage)
	mux.HandleFunc("GET "+a.path("/providers/{provider_id}"), a.providerPage)
	mux.HandleFunc("POST "+a.path("/providers/{provider_id}"), a.updateProvider)
	mux.HandleFunc("POST "+a.path("/providers/{provider_id}/selection"), a.selectProvider)
	mux.HandleFunc("POST "+a.path("/providers/{provider_id}/delete"), a.deleteProvider)
	mux.HandleFunc("GET "+a.path("/mcp"), a.mcpPage)
	mux.HandleFunc("GET "+a.path("/mcp/new"), a.newMCPPage)
	mux.HandleFunc("POST "+a.path("/mcp"), a.createMCP)
	mux.HandleFunc("GET "+a.path("/mcp/{connection_id}"), a.mcpConnectionPage)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}"), a.updateMCP)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/selection"), a.selectMCP)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/delete"), a.deleteMCP)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/diagnostic"), a.diagnoseMCP)
	mux.HandleFunc("POST "+a.path("/mcp/authorization/browser/onboard"), a.onboardMCPBrowser)
	mux.HandleFunc("POST "+a.path("/mcp/authorization/device/onboard"), a.onboardMCPDevice)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/authorization/browser"), a.beginMCPBrowser)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/authorization/device"), a.beginMCPDevice)
	mux.HandleFunc("GET "+a.path("/mcp/authorization/callback"), a.completeMCPBrowser)
	mux.HandleFunc("GET "+a.path("/mcp/authorization/return"), a.mcpAuthorizationReturn)
	mux.HandleFunc("GET "+a.path("/mcp/authorization/device/{attempt_id}"), a.mcpDeviceStatus)
	mux.HandleFunc("POST "+a.path("/mcp/authorization/attempts/{attempt_id}/cancel"), a.cancelMCPAuthorization)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/authorization/disconnect"), a.disconnectMCPAuthorization)
	mux.HandleFunc("POST "+a.path("/mcp/{connection_id}/authorization/retry"), a.retryMCPAuthorization)
	mux.HandleFunc("GET "+a.path("/schedules"), a.schedulesPage)
	mux.HandleFunc("GET "+a.path("/schedules/new"), a.newSchedulePage)
	mux.HandleFunc("POST "+a.path("/schedules"), a.createSchedule)
	mux.HandleFunc("GET "+a.path("/schedules/{schedule_id}"), a.schedulePage)
	mux.HandleFunc("POST "+a.path("/schedules/{schedule_id}"), a.updateSchedule)
	mux.HandleFunc("POST "+a.path("/schedules/{schedule_id}/selection"), a.selectSchedule)
	mux.HandleFunc("POST "+a.path("/schedules/{schedule_id}/delete"), a.deleteSchedule)
	mux.HandleFunc("POST "+a.path("/schedules/{schedule_id}/runs"), a.runSchedule)
	mux.HandleFunc("GET "+a.path("/webhooks"), a.webhooksPage)
	mux.HandleFunc("GET "+a.path("/webhooks/new"), a.newWebhookPage)
	mux.HandleFunc("POST "+a.path("/webhooks"), a.createWebhook)
	mux.HandleFunc("GET "+a.path("/webhooks/{webhook_name}"), a.webhookPage)
	mux.HandleFunc("POST "+a.path("/webhooks/{webhook_name}"), a.updateWebhook)
	mux.HandleFunc("POST "+a.path("/webhooks/{webhook_name}/selection"), a.selectWebhook)
	mux.HandleFunc("POST "+a.path("/webhooks/{webhook_name}/delete"), a.deleteWebhook)
	mux.HandleFunc("POST "+a.path("/webhooks/{webhook_name}/rotate"), a.rotateWebhook)
	mux.HandleFunc("POST "+a.path("/webhooks/{webhook_name}/test"), a.testWebhook)
	mux.HandleFunc("GET "+a.path("/aliases"), a.aliasesPage)
	mux.HandleFunc("GET "+a.path("/aliases/new"), a.newAliasPage)
	mux.HandleFunc("POST "+a.path("/aliases"), a.createAlias)
	mux.HandleFunc("GET "+a.path("/aliases/{alias_name}"), a.aliasPage)
	mux.HandleFunc("POST "+a.path("/aliases/{alias_name}"), a.retargetAlias)
	mux.HandleFunc("POST "+a.path("/aliases/{alias_name}/delete"), a.deleteAlias)
	mux.HandleFunc("GET "+a.path("/hooks"), a.hooksPage)
	mux.HandleFunc("GET "+a.path("/hooks/new"), a.newHookPage)
	mux.HandleFunc("POST "+a.path("/hooks"), a.createHook)
	mux.HandleFunc("GET "+a.path("/hooks/{hook_name}"), a.hookPage)
	mux.HandleFunc("POST "+a.path("/hooks/{hook_name}"), a.updateHook)
	mux.HandleFunc("POST "+a.path("/hooks/{hook_name}/delete"), a.deleteHook)
	mux.HandleFunc("GET "+a.path("/plugins"), a.pluginsPage)
	mux.HandleFunc("GET "+a.path("/plugins/install"), a.pluginInstallPage)
	mux.HandleFunc("POST "+a.path("/plugins/install"), a.installPlugin)
	mux.HandleFunc("GET "+a.path("/plugins/{plugin_name}"), a.pluginPage)
	mux.HandleFunc("GET "+a.path("/plugins/{plugin_name}/review"), a.pluginReviewPage)
	mux.HandleFunc("POST "+a.path("/plugins/{plugin_name}/lifecycle"), a.mutatePlugin)
	mux.HandleFunc("GET "+a.path("/plugin-marketplaces"), a.pluginMarketplacesPage)
	mux.HandleFunc("GET "+a.path("/plugin-marketplaces/new"), a.newPluginMarketplacePage)
	mux.HandleFunc("POST "+a.path("/plugin-marketplaces"), a.addPluginMarketplace)
	mux.HandleFunc("GET "+a.path("/plugin-marketplaces/{marketplace_name}"), a.pluginMarketplacePage)
	mux.HandleFunc("POST "+a.path("/plugin-marketplaces/{marketplace_name}/refresh"), a.refreshPluginMarketplace)
	mux.HandleFunc("POST "+a.path("/plugin-marketplaces/{marketplace_name}/remove"), a.removePluginMarketplace)
	mux.HandleFunc("GET "+a.path("/skills"), a.skillsPage)
	mux.HandleFunc("GET "+a.path("/skills/install"), a.skillInstallPage)
	mux.HandleFunc("POST "+a.path("/skills/reviews/install"), a.reviewSkillInstall)
	mux.HandleFunc("POST "+a.path("/skills/reviews/accept"), a.acceptSkillReview)
	mux.HandleFunc("POST "+a.path("/skills/refresh"), a.refreshSkills)
	mux.HandleFunc("POST "+a.path("/skills/publication/retry"), a.retrySkillPublication)
	mux.HandleFunc("GET "+a.path("/skills/{skill_id}"), a.skillPage)
	mux.HandleFunc("POST "+a.path("/skills/{skill_id}/reviews/update"), a.reviewSkillUpdate)
	mux.HandleFunc("POST "+a.path("/skills/{skill_id}/lifecycle"), a.mutateSkill)
	return noStore(mux)
}

func (a *App) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != a.path("/") {
		http.NotFound(w, r)
		return
	}
	a.redirect(w, "/overview")
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	csrf, err := a.ensureCSRF(w, r)
	if err != nil {
		a.failure(w, http.StatusInternalServerError)
		return
	}
	a.page(w, http.StatusOK, "Sign in", `<form method="post" action="`+template.HTMLEscapeString(a.path("/login"))+`"><input type="hidden" name="csrf_token" value="`+template.HTMLEscapeString(csrf)+`"><label>Username <input name="username" autocomplete="username" required></label><label>Password <input name="password" type="password" autocomplete="current-password" required></label><button>Sign in</button></form>`)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	username, peer := form.Get("username"), directPeer(r.RemoteAddr)
	allowed, retry := a.limiter.Allow(username, r.RemoteAddr)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Round(time.Second)/time.Second))))
		a.authFailure(w, http.StatusTooManyRequests)
		return
	}
	binding, err := randomToken(32)
	if err != nil {
		a.failure(w, http.StatusInternalServerError)
		return
	}
	password := form.Get("password")
	result, err := a.service.Login(r.Context(), managementv1.LocalLoginRequest{Username: username, Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{BrowserBinding: &binding, ConnectionPeer: &peer, CSRFToken: stringPointer(form.Get("csrf_token"))}})
	password = ""
	if err != nil {
		a.serviceFailure(w, err, true)
		return
	}
	a.limiter.Success(username, r.RemoteAddr)
	if result.Outcome == managementv1.MFARequired && result.MFA != nil {
		a.setCookie(w, mfaCookie, binding, a.path("/"), result.MFA.ExpiresAt, true)
		a.ceremony(w, *result.MFA, a.path("/auth/webauthn/finish"), form.Get("csrf_token"))
		return
	}
	if result.Outcome != managementv1.Authenticated || result.Credentials == nil {
		a.failure(w, http.StatusBadGateway)
		return
	}
	a.setCredentials(w, *result.Credentials)
	a.redirect(w, "/overview")
}

func (a *App) finishLogin(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	binding := cookieValue(r, mfaCookie)
	credential, peer := form.Get("credential"), directPeer(r.RemoteAddr)
	credentials, err := a.service.FinishLogin(r.Context(), managementv1.WebAuthLoginMFAFinishRequest{
		TransactionID: form.Get("transaction"), BrowserBinding: &binding, CSRFToken: stringPointer(form.Get("csrf_token")),
		CredentialResponse: &credential, ConnectionPeer: &peer,
	})
	if err != nil {
		a.serviceFailure(w, err, true)
		return
	}
	a.clearCookie(w, mfaCookie, a.path("/"))
	a.setCredentials(w, credentials)
	a.redirect(w, "/overview")
}

func (a *App) overview(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	system, ok := a.systemOverview(w, r)
	if !ok {
		return
	}
	a.page(w, http.StatusOK, "Backoffice", `<p>Signed in as <strong>`+template.HTMLEscapeString(principal.DisplayName)+`</strong>.</p><p><a href="`+a.path("/account")+`">Account</a> · <a href="`+a.path("/access/users")+`">Access</a> · <a href="`+a.path("/audit")+`">Audit</a> · <a href="`+a.path("/agent")+`">Agent</a> · <a href="`+a.path("/memory")+`">Fact memory</a> · <a href="`+a.path("/chat-sessions")+`">AI Session archives</a> · <a href="`+a.path("/providers")+`">Providers</a> · <a href="`+a.path("/mcp")+`">MCP</a> · <a href="`+a.path("/hooks")+`">Hooks</a> · <a href="`+a.path("/plugins")+`">Plugins</a> · <a href="`+a.path("/skills")+`">Skills</a></p>`+system+a.logoutForm(r))
}

func (a *App) account(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	auth := a.auth(r)
	status, err := a.service.MFAStatus(r.Context(), auth)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	sessions, err := a.service.Sessions(r.Context(), auth)
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	body := `<p>` + template.HTMLEscapeString(principal.Username) + ` · ` + template.HTMLEscapeString(string(principal.Role)) + `</p><p>MFA: ` + strconv.FormatBool(status.Enabled) + `</p>`
	body += `<form method="post" action="` + a.path("/account/password") + `"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(cookieValue(r, csrfCookie)) + `"><input type="password" name="current_password" required><input type="password" name="new_password" minlength="12" maxlength="72" required><button>Change password</button></form>`
	if status.Available {
		action, label := "/account/2fa/enable/start", "Enable passkey"
		if status.Enabled {
			action, label = "/account/2fa/replace/start", "Replace passkey"
		}
		body += `<h2>Two-factor authentication</h2><form method="post" action="` + a.path(action) + `"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(cookieValue(r, csrfCookie)) + `"><input type="password" name="password" required><button>` + label + `</button></form>`
		if status.Enabled {
			body += `<form method="post" action="` + a.path("/account/2fa/disable") + `"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(cookieValue(r, csrfCookie)) + `"><input type="password" name="password" required><button>Disable passkey</button></form>`
		}
	}
	body += `<h2>Sessions</h2><ul>`
	for _, session := range sessions.Sessions {
		body += `<li>` + template.HTMLEscapeString(session.WebAuthSessionID) + ` <form method="post" action="` + a.path("/account/sessions/") + url.PathEscape(session.WebAuthSessionID) + `/revoke"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(cookieValue(r, csrfCookie)) + `"><button>Revoke</button></form></li>`
	}
	a.page(w, http.StatusOK, "Account", body+`</ul>`)
}

func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
	_, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	credentials, err := a.service.Refresh(r.Context(), a.auth(r))
	if err != nil {
		if errors.Is(err, managementclient.ErrUnavailable) || errors.Is(err, managementclient.ErrUnauthorized) {
			a.clearCredentials(w)
		}
		a.serviceFailure(w, err, false)
		return
	}
	a.setCredentials(w, credentials)
	a.redirect(w, "/overview")
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.mutationForm(w, r); !ok {
		return
	}
	if err := a.service.Logout(r.Context(), a.auth(r)); err != nil && !errors.Is(err, managementclient.ErrUnauthorized) {
		a.serviceFailure(w, err, false)
		return
	}
	a.clearCredentials(w)
	a.redirect(w, "/login")
}

func (a *App) replacePassword(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	current, next := form.Get("current_password"), form.Get("new_password")
	credentials, err := a.service.ReplacePassword(r.Context(), a.auth(r), managementv1.WebAuthPasswordReplaceRequest{CurrentPassword: &current, NewPassword: &next})
	current, next = "", ""
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.setCredentials(w, credentials)
	a.redirect(w, "/account")
}

func (a *App) beginMFA(replace bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form, ok := a.mutationForm(w, r)
		if !ok {
			return
		}
		binding, err := randomToken(32)
		if err != nil {
			a.failure(w, 500)
			return
		}
		password := form.Get("password")
		continuation, err := a.service.BeginMFA(r.Context(), a.auth(r), replace, &password, &binding)
		password = ""
		if err != nil {
			a.serviceFailure(w, err, false)
			return
		}
		a.setCookie(w, mfaCookie, binding, a.path("/"), continuation.ExpiresAt, true)
		target := "/account/2fa/enable/finish"
		if replace {
			target = "/account/2fa/replace/finish"
		}
		a.ceremony(w, continuation, a.path(target), form.Get("csrf_token"))
	}
}

func (a *App) finishMFA(replace bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form, ok := a.mutationForm(w, r)
		if !ok {
			return
		}
		binding, credential := cookieValue(r, mfaCookie), form.Get("credential")
		credentials, err := a.service.FinishMFA(r.Context(), a.auth(r), replace, managementv1.WebAuthMFAFinishRequest{TransactionID: form.Get("transaction"), BrowserBinding: &binding, CredentialResponse: &credential})
		if err != nil {
			a.serviceFailure(w, err, false)
			return
		}
		a.clearCookie(w, mfaCookie, a.path("/"))
		a.setCredentials(w, credentials)
		a.redirect(w, "/account")
	}
}

func (a *App) disableMFA(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	password := form.Get("password")
	credentials, err := a.service.DisableMFA(r.Context(), a.auth(r), &password)
	password = ""
	if err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.setCredentials(w, credentials)
	a.redirect(w, "/account")
}

func (a *App) revokeSession(w http.ResponseWriter, r *http.Request) {
	form, ok := a.mutationForm(w, r)
	if !ok {
		return
	}
	if err := a.service.RevokeSession(r.Context(), a.auth(r), r.PathValue("session_id"), form.Get("confirm_current") == formYesValue); err != nil {
		a.serviceFailure(w, err, false)
		return
	}
	a.redirect(w, "/account")
}

func (a *App) principal(w http.ResponseWriter, r *http.Request) (managementv1.WebAuthPrincipal, bool) {
	principal, err := a.service.Principal(r.Context(), a.auth(r))
	if err != nil {
		if errors.Is(err, managementclient.ErrUnauthorized) {
			a.clearCredentials(w)
			a.redirect(w, "/login")
		} else {
			a.serviceFailure(w, err, false)
		}
		return managementv1.WebAuthPrincipal{}, false
	}
	return principal, true
}

func (a *App) mutationForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	return a.mutationFormLimit(w, r, maxFormBytes)
}

func (a *App) mutationFormLimit(w http.ResponseWriter, r *http.Request, limit int64) (url.Values, bool) {
	if r.Header.Get("Origin") != a.origin {
		a.failure(w, http.StatusForbidden)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseForm(); err != nil {
		a.failure(w, http.StatusBadRequest)
		return nil, false
	}
	provided, expected := r.Form.Get("csrf_token"), cookieValue(r, csrfCookie)
	if provided == "" || expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		a.failure(w, http.StatusForbidden)
		return nil, false
	}
	return r.Form, true
}

func (a *App) auth(r *http.Request) managementclient.Auth {
	return managementclient.Auth{Access: cookieValue(r, accessCookie), Refresh: cookieValue(r, refreshCookie), CSRF: cookieValue(r, csrfCookie)}
}

func (a *App) ensureCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	if value := cookieValue(r, csrfCookie); value != "" {
		return value, nil
	}
	value, err := randomToken(32)
	if err == nil {
		a.setCookie(w, csrfCookie, value, a.path("/"), time.Time{}, false)
	}
	return value, err
}

func (a *App) setCredentials(w http.ResponseWriter, credentials managementv1.WebAuthCredentials) {
	a.setCookie(w, accessCookie, credentials.AccessToken, a.path("/"), credentials.AccessExpiresAt, true)
	a.setCookie(w, refreshCookie, credentials.RefreshToken, a.path("/auth/session/refresh"), credentials.RefreshExpiresAt, true)
	a.setCookie(w, csrfCookie, credentials.CSRFToken, a.path("/"), credentials.RefreshExpiresAt, false)
}

func (a *App) clearCredentials(w http.ResponseWriter) {
	a.clearCookie(w, accessCookie, a.path("/"))
	a.clearCookie(w, refreshCookie, a.path("/auth/session/refresh"))
	a.clearCookie(w, csrfCookie, a.path("/"))
	a.clearCookie(w, mfaCookie, a.path("/"))
}
func (a *App) setCookie(w http.ResponseWriter, name, value, path string, expires time.Time, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, Expires: expires, MaxAge: 0, HttpOnly: httpOnly, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}
func (a *App) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: path, Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: name != csrfCookie, Secure: a.secure, SameSite: http.SameSiteStrictMode})
}
func (a *App) path(suffix string) string { return a.mount + suffix }
func (a *App) redirect(w http.ResponseWriter, suffix string) {
	w.Header().Set("Location", a.path(suffix))
	w.WriteHeader(http.StatusSeeOther)
}
func (a *App) logoutForm(r *http.Request) string {
	return `<form method="post" action="` + a.path("/logout") + `"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(cookieValue(r, csrfCookie)) + `"><button>Sign out</button></form>`
}
func (a *App) page(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>`+template.HTMLEscapeString(title)+` · Balda</title></head><body><main><h1>`+template.HTMLEscapeString(title)+`</h1>`+body+`</main></body></html>`)
}
func (a *App) failure(w http.ResponseWriter, status int) {
	a.page(w, status, "Request failed", `<p>The request could not be completed.</p>`)
}
func (a *App) authFailure(w http.ResponseWriter, status int) {
	a.page(w, status, "Sign in", `<p>Sign-in failed. Check your credentials and try again.</p>`)
}
func (a *App) serviceFailure(w http.ResponseWriter, err error, login bool) {
	if login && (errors.Is(err, managementclient.ErrUnauthorized) || errors.Is(err, managementclient.ErrInvalid)) {
		a.authFailure(w, http.StatusUnauthorized)
		return
	}
	switch {
	case errors.Is(err, managementclient.ErrUnauthorized):
		a.failure(w, http.StatusUnauthorized)
	case errors.Is(err, managementclient.ErrForbidden):
		a.failure(w, http.StatusForbidden)
	case errors.Is(err, managementclient.ErrConflict):
		a.failure(w, http.StatusConflict)
	case errors.Is(err, managementclient.ErrUnavailable):
		a.failure(w, http.StatusServiceUnavailable)
	default:
		a.failure(w, http.StatusInternalServerError)
	}
}

func (a *App) ceremony(w http.ResponseWriter, continuation managementv1.WebAuthMFAContinuation, action, csrf string) {
	options, err := json.Marshal(continuation.PublicKeyOptions)
	if err != nil {
		a.failure(w, 500)
		return
	}
	registration := strings.Contains(action, "/account/2fa/")
	body := `<form method="post" action="` + template.HTMLEscapeString(action) + `" data-webauthn data-registration="` + strconv.FormatBool(registration) + `" data-options="` + template.HTMLEscapeString(string(options)) + `"><input type="hidden" name="csrf_token" value="` + template.HTMLEscapeString(csrf) + `"><input type="hidden" name="transaction" value="` + template.HTMLEscapeString(continuation.TransactionID) + `"><input type="hidden" name="credential"><p data-webauthn-status>Continue with your passkey.</p><button data-webauthn-button disabled>Continue</button></form><script>` + webAuthnScript + `</script>`
	a.page(w, 200, "Passkey verification", body)
}

const webAuthnScript = `(()=>{const d=v=>Uint8Array.from(atob(v.replace(/-/g,"+").replace(/_/g,"/")),c=>c.charCodeAt(0)),e=v=>v==null?null:btoa(String.fromCharCode(...new Uint8Array(v))).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,""),f=document.querySelector("[data-webauthn]"),b=f.querySelector("button"),s=f.querySelector("[data-webauthn-status]");if(!window.PublicKeyCredential||!navigator.credentials||!window.isSecureContext){s.textContent="Passkeys require a secure supported browser.";return}b.disabled=false;f.addEventListener("submit",async n=>{n.preventDefault();b.disabled=true;try{const r=f.dataset.registration==="true",o=JSON.parse(f.dataset.options).publicKey;o.challenge=d(o.challenge);if(o.user)o.user.id=d(o.user.id);for(const k of ["allowCredentials","excludeCredentials"])if(o[k])o[k]=o[k].map(i=>({...i,id:d(i.id)}));const c=await navigator.credentials[r?"create":"get"]({publicKey:o}),p=c.response,x={id:c.id,rawId:e(c.rawId),type:c.type,clientExtensionResults:c.getClientExtensionResults(),response:{clientDataJSON:e(p.clientDataJSON)}};if(r){x.response.attestationObject=e(p.attestationObject);x.response.transports=p.getTransports?p.getTransports():[]}else{x.response.authenticatorData=e(p.authenticatorData);x.response.signature=e(p.signature);x.response.userHandle=e(p.userHandle)}f.elements.credential.value=JSON.stringify(x);HTMLFormElement.prototype.submit.call(f)}catch(_){s.textContent="Verification was cancelled or failed.";b.disabled=false}})})();`

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}
func directPeer(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}
func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
