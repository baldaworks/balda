package balda

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutefx"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type browserWebhookRoutes struct{ store state.WebhookRouteStore }

func (r browserWebhookRoutes) LookupActiveManagedByName(ctx context.Context, name string) (webhookroutecmd.Record, bool, error) {
	record, found, err := r.store.LookupActiveManagedByName(ctx, name)
	return browserWebhookRecord(record), found, err
}

func (r browserWebhookRoutes) Get(ctx context.Context, name string) (webhookroutecmd.Record, bool, error) {
	record, found, err := r.store.Get(ctx, name)
	return browserWebhookRecord(record), found, err
}

func browserWebhookRecord(r state.WebhookRouteRecord) webhookroutecmd.Record {
	return webhookroutecmd.Record{Name: r.Name, Source: r.Source,
		PromptTemplate: r.PromptTemplate, ReportToKind: r.ReportToKind, ReportToKey: r.ReportToKey,
		AckOnDelivery: r.AckOnDelivery, DedupeSource: r.DedupeSource, DedupeHeader: r.DedupeHeader,
		AuthType: r.AuthType, AuthHeader: r.AuthHeader, SecretVerifier: r.SecretVerifier,
		Enabled: r.Enabled, Deleted: r.Deleted, Version: r.Version}
}

func browserLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

func browserWebhookPublisher(provider state.Provider) webhookapp.JobPublisher {
	return webhookapp.JobPublisherFunc(func(ctx context.Context, payload turncmd.SessionTurnPayload,
		routeName, requestID string) (*actortransport.DispatchReceipt, string, error) {
		_, jobID, err := turncmd.WebhookJobEnvelope(payload, routeName, requestID)
		if err != nil {
			return nil, "", err
		}
		if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: jobID,
			SessionID: payload.Locator.SessionID, Objective: payload.Text,
			Status: state.JobStatusCompleted, PrivateRunKind: state.PrivateRunKindWebhook}); err != nil {
			return nil, "", err
		}
		if err := provider.Jobs().RecordPrivateOutput(ctx, jobID, "Processed: "+payload.Text, false); err != nil {
			return nil, "", err
		}
		if payload.ReportTo != nil {
			deliveryKey := jobID + ":delivery:final"
			if _, _, err := provider.Jobs().ReserveDelivery(ctx, state.DeliveryRecord{
				ID: "browser-" + jobID, DeliveryKey: deliveryKey, JobID: jobID,
				SessionID: payload.Locator.SessionID, Channel: payload.ReportTo.ChannelType,
				AddressKey: payload.ReportTo.AddressKey, Kind: "delivery",
				Payload: "Processed: " + payload.Text, PayloadHash: jobID,
			}); err != nil {
				return nil, "", err
			}
			if err := provider.Jobs().MarkDeliverySent(ctx, deliveryKey, "synthetic-provider-message"); err != nil {
				return nil, "", err
			}
		}
		return &actortransport.DispatchReceipt{MsgID: "browser-" + jobID, Stream: "balda.cmd.job", Sequence: 1}, jobID, nil
	})
}

// TestBackofficeWebhooksBrowserWorkflow checks the live HTTP and durable route
// seams through ordinary administrator login. Only the model job publisher is
// replaced so the browser gate does not depend on an external model provider.
func TestBackofficeWebhooksBrowserWorkflow(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, basePath := range []string{"", "/balda"} {
		t.Run(basePath, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			provider, err := state.NewSQLiteProvider(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			hash, err := userpassword.Hash([]byte("correct horse battery staple"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			for _, role := range []usercmd.Role{usercmd.RoleAdministrator, usercmd.RoleOperator} {
				id := string(role)
				user := usercmd.User{ID: id, Username: id, NormalizedUsername: id,
					DisplayName: id, Role: role, Status: usercmd.StatusActive, Primary: role == usercmd.RoleAdministrator,
					Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
					Version:    1, CreatedAt: now, UpdatedAt: now}
				if err := provider.Users().CreateUser(t.Context(), user,
					usercmd.CredentialSecret{UserID: id, PasswordHash: hash},
					usercmd.AuditEvent{ID: "webhook-browser-" + id, Action: usercmd.AuditActionUserCreated,
						Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser,
						TargetID: id, Source: "webhook-browser-fixture", OccurredAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			manager := webhookmanagement.New(webhookroutefx.NewStore(provider))
			configured := webhookroutecmd.ConfiguredRoute{Name: "configured",
				PromptTemplate: "Configured: {{.RawBody}}", DedupeSource: webhookroutecmd.DedupeSourceRequestID,
				AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: "X-Configured-Secret", Enabled: true}
			if err := manager.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{configured}); err != nil {
				t.Fatal(err)
			}
			publisher := browserWebhookPublisher(provider)
			resolver := webhookapp.TargetResolverFunc(func(_ context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
				if target.Target != "locator" || target.Key != "telegram:9001:0" {
					return envelopetarget.Resolved{}, errors.New("unknown synthetic report locator")
				}
				locator, err := locatorref.Parse(target.Key)
				if err != nil {
					return envelopetarget.Resolved{}, err
				}
				return envelopetarget.Resolved{Locator: locator}, nil
			})
			service := webhookapp.NewService(resolver, provider.WebhookAdmissions(), publisher)
			ingress, err := webhookapp.NewIngress(basePath, []webhookapp.ConfiguredRoute{{Name: configured.Name,
				PromptTemplate: configured.PromptTemplate,
				AuthType:       configured.AuthType, AuthHeader: configured.AuthHeader,
				AuthValue: "synthetic-config-secret", DedupeSource: configured.DedupeSource}},
				browserWebhookRoutes{store: provider.WebhookRoutes()}, service)
			if err != nil {
				t.Fatal(err)
			}
			webhookAddress := browserLoopbackAddress(t)
			receiver, err := webhook.NewReceiver(webhook.Config{Enabled: true, BasePath: basePath, ListenAddr: webhookAddress,
				Routes: map[string]webhook.RouteConfig{"configured": {
					PromptTemplate: configured.PromptTemplate, Auth: webhook.RouteAuthConfig{Type: "header",
						Header: configured.AuthHeader, Value: "synthetic-config-secret"}}}}, ingress, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = receiver.Stop(context.Background()) })
			backofficeAddress := browserLoopbackAddress(t)
			runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{
				ListenAddr: backofficeAddress, PublicURL: "http://" + backofficeAddress, BasePath: basePath,
				AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, provider)
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.ConfigureWebhooksOperations(webhookbackofficeapp.New(manager, ingress,
				provider.WebhookAdmissions())); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Stop(context.Background()) })
			root, err := filepath.Abs("../../..")
			if err != nil {
				t.Fatal(err)
			}
			secretPath := filepath.Join(t.TempDir(), "restart-secret")
			command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/webhooks.cjs",
				"http://"+backofficeAddress+basePath, "http://"+webhookAddress, "initial", secretPath)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("Webhooks browser workflow: %v\n%s", err, output)
			}
			restartSecret, err := os.ReadFile(secretPath)
			if err != nil || len(restartSecret) < 32 {
				t.Fatalf("read one-time restart fixture secret: %v", err)
			}
			if err := os.Remove(secretPath); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"browser-desktop", "browser-mobile"} {
				route, found, err := provider.WebhookRoutes().Get(t.Context(), name)
				if err != nil || !found || !route.Deleted || route.Source != state.WebhookRouteSourceManaged {
					t.Fatalf("route %q not archived: %+v, found=%t, err=%v", name, route, found, err)
				}
				history, err := provider.WebhookAdmissions().ListHistory(t.Context(), name, time.Time{}, "", 100)
				if err != nil || len(history) < 2 {
					t.Fatalf("route %q history: %d entries, err=%v", name, len(history), err)
				}
			}
			persistent, found, err := provider.WebhookRoutes().Get(t.Context(), "persistent")
			if err != nil || !found || !persistent.Enabled || persistent.Deleted || persistent.Source != state.WebhookRouteSourceManaged {
				t.Fatalf("live restart route = %+v, found=%t, err=%v", persistent, found, err)
			}
			t.Log(string(output))
			if err := runtime.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := receiver.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := state.NewSQLiteProvider(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedManager := webhookmanagement.New(webhookroutefx.NewStore(reopened))
			if err := reopenedManager.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{configured}); err != nil {
				t.Fatal(err)
			}
			reopenedIngress, err := webhookapp.NewIngress(basePath, []webhookapp.ConfiguredRoute{{Name: configured.Name,
				PromptTemplate: configured.PromptTemplate,
				AuthType:       configured.AuthType, AuthHeader: configured.AuthHeader,
				AuthValue: "synthetic-config-secret", DedupeSource: configured.DedupeSource}},
				browserWebhookRoutes{store: reopened.WebhookRoutes()},
				webhookapp.NewService(resolver, reopened.WebhookAdmissions(), browserWebhookPublisher(reopened)))
			if err != nil {
				t.Fatal(err)
			}
			reopenedReceiver, err := webhook.NewReceiver(webhook.Config{Enabled: true, BasePath: basePath, ListenAddr: webhookAddress,
				Routes: map[string]webhook.RouteConfig{"configured": {
					PromptTemplate: configured.PromptTemplate, Auth: webhook.RouteAuthConfig{Type: "header",
						Header: configured.AuthHeader, Value: "synthetic-config-secret"}}}}, reopenedIngress, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			if err := reopenedReceiver.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedReceiver.Stop(context.Background()) })
			reopenedRuntime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{
				ListenAddr: backofficeAddress, PublicURL: "http://" + backofficeAddress, BasePath: basePath,
				AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour}}, reopened)
			if err != nil {
				t.Fatal(err)
			}
			if err := reopenedRuntime.ConfigureWebhooksOperations(webhookbackofficeapp.New(reopenedManager,
				reopenedIngress, reopened.WebhookAdmissions())); err != nil {
				t.Fatal(err)
			}
			if err := reopenedRuntime.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedRuntime.Stop(context.Background()) })
			restartCommand := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/webhooks.cjs",
				"http://"+backofficeAddress+basePath, "http://"+webhookAddress, "restart")
			restartCommand.Dir = root
			restartCommand.Env = append(os.Environ(), "BALDA_WEBHOOK_RESTART_SECRET="+string(restartSecret))
			restartOutput, err := restartCommand.CombinedOutput()
			if err != nil {
				t.Fatalf("Webhooks restart browser workflow: %v\n%s", err, restartOutput)
			}
			t.Log(string(restartOutput))
		})
	}
}
