package balda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
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
	executor    *sessionturnapp.TurnExecutionService
	sessions    adksession.Service
	invocations *atomic.Int64
}

const (
	browserWebhookAppName   = "webhook-browser-fixture"
	browserManagedAliasName = "main_chat"
	browserManagedOutput    = "Processed: Actor: alias input"
)

func (r browserActorTurnRunner) RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error {
	agent, err := adkagent.New(adkagent.Config{Name: "webhook-browser-fixture",
		Description: "deterministic private webhook provider",
		Run: func(invocation adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				r.invocations.Add(1)
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
	runner, err := adkrunner.New(adkrunner.Config{AppName: browserWebhookAppName, Agent: agent, SessionService: r.sessions})
	if err != nil {
		return err
	}
	session, err := r.sessions.Create(ctx, &adksession.CreateRequest{
		AppName: browserWebhookAppName, UserID: payload.UserID, SessionID: payload.Locator.SessionID,
	})
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

type browserDeliveryTransport struct {
	sent     atomic.Int64
	payloads chan deliverycmd.Payload
}

func (d *browserDeliveryTransport) Dispatch(_ context.Context, delivery deliveryworkflow.Delivery) (string, error) {
	if delivery.Payload.Text == "" {
		return "", fmt.Errorf("empty test delivery")
	}
	d.sent.Add(1)
	d.payloads <- delivery.Payload
	return "synthetic-provider-message", nil
}

type browserRunSessionCloser struct{ sessions adksession.Service }

func (c browserRunSessionCloser) CloseRunSession(ctx context.Context, sessionID, userID string) error {
	return c.sessions.Delete(ctx, &adksession.DeleteRequest{
		AppName: browserWebhookAppName, UserID: userID, SessionID: sessionID,
	})
}

func TestWebhookActorRuntimeFromHTTP(t *testing.T) {
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
	sessions := adksession.InMemoryService()
	var invocations atomic.Int64
	delivery := &browserDeliveryTransport{payloads: make(chan deliverycmd.Payload, 2)}
	registry := dispatch.NewMemoryRegistry()
	for _, actor := range []dispatch.Actor{
		actors.NewJobActorExecutor(jobexec.New(lifecycle, bus)),
		actors.NewSessionActor(actors.SessionActorConfig{Turns: turns,
			Runner: browserActorTurnRunner{executor: turnExecutor, sessions: sessions, invocations: &invocations},
			Tasks:  lifecycle, Dispatcher: bus}),
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
	var managedRef atomic.Value
	managedRef.Store("")
	publisher := webhookapp.JobPublisherFunc(func(ctx context.Context, payload turncmd.SessionTurnPayload,
		routeName, requestID string) (*actortransport.DispatchReceipt, string, error) {
		if routeName == "managed" {
			managedRef.Store("telegram:9003:0")
		}
		env, jobID, err := turncmd.WebhookJobEnvelope(payload, routeName, requestID)
		if err != nil {
			return nil, "", err
		}
		receipt, err := bus.Dispatch(ctx, env)
		return receipt, jobID, err
	})
	destinations := auth.NewDestinationResolverWithManagedAliases(nil, nil,
		func(_ context.Context, name string) (deliverycmd.Locator, error) {
			if name != browserManagedAliasName || managedRef.Load().(string) == "" {
				return deliverycmd.Locator{}, aliascmd.ErrNotFound
			}
			return locatorref.Parse(managedRef.Load().(string))
		})
	resolver := webhookapp.TargetResolverFunc(func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
		return envelopetarget.Resolve(ctx, destinations, target)
	})
	service := webhookapp.NewService(resolver, provider.WebhookAdmissions(), publisher)
	configured := webhookapp.ConfiguredRoute{Name: "actor",
		PromptTemplate: "Actor: {{.RawBody}}", ReportToKind: "locator", ReportToKey: "telegram:9001:0",
		AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: "X-Actor-Secret", AuthValue: "synthetic-actor-secret",
		DedupeSource: webhookroutecmd.DedupeSourceRequestID}
	managed := configured
	managed.Name = "managed"
	managed.ReportToKind, managed.ReportToKey = envelopetarget.TargetManagedAlias, browserManagedAliasName
	ingress, err := webhookapp.NewIngress("", []webhookapp.ConfiguredRoute{configured, managed}, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	address := browserLoopbackAddress(t)
	receiver, err := webhook.NewReceiver(webhook.Config{Enabled: true, ListenAddr: address,
		Routes: map[string]webhook.RouteConfig{"actor": {PromptTemplate: configured.PromptTemplate, Envelope: webhook.RouteEnvelopeConfig{
			ReportTo: &webhook.RouteTargetConfig{Target: "locator", Key: "telegram:9001:0"}},
			Auth: webhook.RouteAuthConfig{Type: "header", Header: configured.AuthHeader,
				Value: configured.AuthValue}},
			"managed": {PromptTemplate: managed.PromptTemplate, Envelope: webhook.RouteEnvelopeConfig{
				ReportTo: &webhook.RouteTargetConfig{Target: envelopetarget.TargetManagedAlias, Key: browserManagedAliasName}},
				Auth: webhook.RouteAuthConfig{Type: "header", Header: managed.AuthHeader,
					Value: managed.AuthValue}}}}, ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Stop(context.Background()) })
	post := func(route, requestID, body string) *http.Response {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+address+"/webhooks/"+route, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Actor-Secret", configured.AuthValue)
		request.Header.Set("X-Request-Id", requestID)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := post("actor", "actor-request-1", "actor input")
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
	firstDelivery := <-delivery.payloads
	if firstDelivery.Locator.AddressKey != "9001:0" || invocations.Load() != 1 {
		t.Fatalf("literal route delivery = %+v, provider calls=%d", firstDelivery, invocations.Load())
	}

	missing := post("managed", "missing-alias-request", "missing alias input")
	if missing.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(missing.Body)
		t.Fatalf("missing alias status = %d, body=%s", missing.StatusCode, body)
	}
	if err := missing.Body.Close(); err != nil {
		t.Fatal(err)
	}
	missingHistory, err := provider.WebhookAdmissions().ListHistory(ctx, "managed", time.Time{}, "", 10)
	if err != nil || len(missingHistory) != 0 || invocations.Load() != 1 {
		t.Fatalf("missing alias admitted work: history=%+v provider calls=%d err=%v", missingHistory, invocations.Load(), err)
	}

	managedRef.Store("telegram:9002:0")
	managedResponse := post("managed", "managed-alias-request", "alias input")
	defer func() { _ = managedResponse.Body.Close() }()
	if managedResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(managedResponse.Body)
		t.Fatalf("managed alias admission status = %d, body=%s", managedResponse.StatusCode, body)
	}
	var managedAccepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(managedResponse.Body).Decode(&managedAccepted); err != nil || managedAccepted.JobID == "" {
		t.Fatalf("managed alias receipt = %+v, err=%v", managedAccepted, err)
	}
	for {
		history, found, err := provider.WebhookAdmissions().GetHistory(ctx, "managed", managedAccepted.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if found && history.JobStatus == state.JobStatusCompleted && history.DeliveryStatus == state.DeliveryStatusSent {
			if history.RawBody == nil || *history.RawBody != "alias input" ||
				history.Output != browserManagedOutput ||
				history.ReportTo == nil || history.ReportTo.AddressKey != "9002:0" ||
				!strings.Contains(history.DeliveryPayload, browserManagedOutput) ||
				managedRef.Load().(string) != "telegram:9003:0" || invocations.Load() != 2 {
				t.Fatalf("managed alias outcome = %+v, current alias=%s provider calls=%d", history,
					managedRef.Load().(string), invocations.Load())
			}
			break
		}
		select {
		case err := <-runtimeDone:
			t.Fatalf("actor runtime stopped before managed alias outcome: %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for managed alias outcome: %+v", history)
		case <-time.After(25 * time.Millisecond):
		}
	}
	managedDelivery := <-delivery.payloads
	if managedDelivery.Locator.AddressKey != "9002:0" ||
		managedDelivery.Text != browserManagedOutput || delivery.sent.Load() != 2 {
		t.Fatalf("managed alias delivery = %+v, sends=%d", managedDelivery, delivery.sent.Load())
	}
	admission, found, err := provider.WebhookAdmissions().GetByJobID(ctx, managedAccepted.JobID)
	if err != nil || !found {
		t.Fatalf("read managed alias admission: found=%t err=%v", found, err)
	}
	if _, err := sessions.Get(ctx, &adksession.GetRequest{
		AppName: browserWebhookAppName, UserID: admission.SessionID, SessionID: admission.SessionID,
	}); err != nil {
		t.Fatalf("private provider session before cleanup: %v", err)
	}
	finalizer := jobexec.NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), browserRunSessionCloser{sessions: sessions}, bus, zerolog.Nop())
	if err := finalizer.Settle(ctx, 100); err != nil {
		t.Fatalf("close private webhook sessions: %v", err)
	}
	if _, err := sessions.Get(ctx, &adksession.GetRequest{
		AppName: browserWebhookAppName, UserID: admission.SessionID, SessionID: admission.SessionID,
	}); !errors.Is(err, adksession.ErrNotFound) {
		t.Fatalf("private provider session after cleanup: %v", err)
	}
	job, found, err := provider.Jobs().GetJob(ctx, managedAccepted.JobID)
	if err != nil || !found || job.PrivateRunClosedAt == "" ||
		job.Objective != "Actor: alias input" || job.Result != browserManagedOutput {
		t.Fatalf("retained managed alias job = %+v, found=%t err=%v", job, found, err)
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
