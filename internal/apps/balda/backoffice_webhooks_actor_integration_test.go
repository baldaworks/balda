package balda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/eventbus"
	natsbus "github.com/baldaworks/balda/internal/apps/balda/eventbus/nats"
	"github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/jobexec"
	"github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actorengine "github.com/baldaworks/go-actorlayer/engine"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
	adkagent "google.golang.org/adk/v2/agent"
	adkrunner "google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type browserActorTurnRunner struct {
	executor *sessionturnapp.TurnExecutionService
}

func (r browserActorTurnRunner) RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error {
	agent, err := adkagent.New(adkagent.Config{Name: "webhook-browser-fixture",
		Description: "deterministic private webhook provider",
		Run: func(invocation adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				result := adksession.NewEvent(ctx, invocation.InvocationID())
				result.Content = genai.NewContentFromText("Processed: "+payload.Text, genai.RoleModel)
				result.TurnComplete = true
				yield(result, nil)
			}
		},
	})
	if err != nil {
		return err
	}
	sessions := adksession.InMemoryService()
	const appName = "webhook-browser-fixture"
	runner, err := adkrunner.New(adkrunner.Config{AppName: appName, Agent: agent, SessionService: sessions})
	if err != nil {
		return err
	}
	session, err := sessions.Create(ctx, &adksession.CreateRequest{AppName: appName, UserID: payload.UserID})
	if err != nil {
		return err
	}
	request := sessionturnapp.ExecutionRequest{Text: payload.Text, Runner: runner,
		UserID: payload.UserID, AgentSessionID: session.Session.ID(),
		SessionID: payload.Locator.SessionID, JobID: payload.JobID,
		Locator: payload.Locator, TurnSource: payload.Source,
		Deliver: payload.Deliver, DedupeKey: payload.DedupeKey}
	if payload.ReportTo != nil {
		request.DeliveryLocator = *payload.ReportTo
	}
	return r.executor.Execute(ctx, request)
}

type browserDeliveryTransport struct{ sent atomic.Int64 }

func (d *browserDeliveryTransport) Dispatch(_ context.Context, delivery deliveryworkflow.Delivery) (string, error) {
	if delivery.Payload.Text == "" {
		return "", fmt.Errorf("empty test delivery")
	}
	d.sent.Add(1)
	return "synthetic-provider-message", nil
}

func TestWebhookActorRuntimeFromHTTP(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("set BALDA_BACKOFFICE_BROWSER_TEST=1 for the webhook runtime integration gate")
	}
	if testing.Short() {
		t.Skip("embedded actor bus and browser fixture are integration-only")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	bus, err := natsbus.NewBus(natsbus.Params{LC: fxtest.NewLifecycle(t),
		Config: eventbus.Config{Embedded: true}, Execution: execution.Config{},
		StateDir: t.TempDir(), Logger: zerolog.Nop()})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Drain(context.Background()) })
	lifecycle, err := jobs.NewJobLifecycleServiceForTests(provider.Jobs(), bus)
	if err != nil {
		t.Fatal(err)
	}
	turns := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() { _ = turns.Shutdown(context.Background()) })
	turnExecutor := sessionturnapp.NewTurnExecutionServiceWithJobEventsAndCapture(bus, nil, nil,
		zerolog.Nop(), automode.DefaultMaxTurns, nil)
	turnExecutor.SetPrivateOutputRecorder(lifecycle)
	turnExecutor.SetWebhookTurnClaimer(lifecycle)
	delivery := &browserDeliveryTransport{}
	registry := dispatch.NewMemoryRegistry()
	for _, actor := range []dispatch.Actor{
		actors.NewJobActorExecutor(jobexec.New(lifecycle, bus)),
		actors.NewSessionActor(actors.SessionActorConfig{Turns: turns,
			Runner: browserActorTurnRunner{executor: turnExecutor}, Tasks: lifecycle, Dispatcher: bus}),
		actors.NewJobDeliveryActor(deliveryworkflow.New(delivery, provider.Jobs(), nil, nil, bus, zerolog.Nop())),
	} {
		if err := registry.Register(actor); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := actorengine.NewDispatchRuntime(actorengine.RuntimeConfig{Registry: registry,
		AddressOf: func(env actorlayer.Envelope) (string, error) {
			return env.To.Target + ":" + env.To.Key, nil
		},
		LaneKey: func(env actorlayer.Envelope) string { return env.To.Target + ":" + env.To.Key },
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtime.Run(runCtx, bus) }()
	publisher := webhookapp.JobPublisherFunc(func(ctx context.Context, payload turncmd.SessionTurnPayload,
		routeName, requestID string) (*actortransport.DispatchReceipt, string, error) {
		env, jobID, err := turncmd.WebhookJobEnvelope(payload, routeName, requestID)
		if err != nil {
			return nil, "", err
		}
		receipt, err := bus.Dispatch(ctx, env)
		return receipt, jobID, err
	})
	resolver := webhookapp.TargetResolverFunc(func(_ context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
		locator, err := locatorref.Parse(target.Key)
		return envelopetarget.Resolved{Locator: locator}, err
	})
	service := webhookapp.NewService(resolver, provider.WebhookAdmissions(), publisher)
	configured := webhookapp.ConfiguredRoute{Name: "actor",
		PromptTemplate: "Actor: {{.RawBody}}", ReportToKind: "locator", ReportToKey: "telegram:9001:0",
		AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: "X-Actor-Secret", AuthValue: "synthetic-actor-secret",
		DedupeSource: webhookroutecmd.DedupeSourceRequestID}
	ingress, err := webhookapp.NewIngress("", []webhookapp.ConfiguredRoute{configured}, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	address := browserLoopbackAddress(t)
	receiver, err := webhook.NewReceiver(webhook.Config{Enabled: true, ListenAddr: address,
		Routes: map[string]webhook.RouteConfig{"actor": {PromptTemplate: configured.PromptTemplate, Envelope: webhook.RouteEnvelopeConfig{
			ReportTo: &webhook.RouteTargetConfig{Target: "locator", Key: "telegram:9001:0"}},
			Auth: webhook.RouteAuthConfig{Type: "header", Header: configured.AuthHeader,
				Value: configured.AuthValue}}}}, ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Stop(context.Background()) })
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/webhooks/actor",
		bytes.NewBufferString("actor input"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Actor-Secret", configured.AuthValue)
	request.Header.Set("X-Request-Id", "actor-request-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP admission status = %d, body=%s", response.StatusCode, body)
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil || accepted.JobID == "" {
		t.Fatalf("HTTP admission receipt = %+v, err=%v", accepted, err)
	}
	for {
		history, found, err := provider.WebhookAdmissions().GetHistory(ctx, "actor", accepted.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if found && history.JobStatus == state.JobStatusCompleted && history.DeliveryStatus == state.DeliveryStatusSent {
			if history.Output != "Processed: Actor: actor input" ||
				history.ReportTo == nil || history.ReportTo.AddressKey != "9001:0" ||
				delivery.sent.Load() != 1 || history.RawBody == nil || *history.RawBody != "actor input" ||
				!strings.Contains(history.DeliveryPayload, "Processed: Actor: actor input") {
				t.Fatalf("integrated actor outcome = %+v, sends=%d", history, delivery.sent.Load())
			}
			break
		}
		select {
		case err := <-runtimeDone:
			t.Fatalf("actor runtime stopped before completion: %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for actor outcome: %+v", history)
		case <-time.After(25 * time.Millisecond):
		}
	}
	stop()
	select {
	case err := <-runtimeDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("stop actor runtime: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actor runtime did not stop")
	}
}
