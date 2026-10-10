package balda

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

type testSharedTelegramSource struct {
	address  string
	shared   bool
	started  bool
	startErr error
}

func (s *testSharedTelegramSource) UpdateChan() <-chan client.Update { return nil }
func (s *testSharedTelegramSource) HTTPCallback() (http.Handler, string) {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }), "/old/telegram"
}
func (s *testSharedTelegramSource) UseSharedListener() error { s.shared = true; return nil }
func (s *testSharedTelegramSource) Start(context.Context) error {
	if !s.shared {
		return errors.New("source did not select shared listener")
	}
	connection, err := net.DialTimeout("tcp", s.address, time.Second)
	if err != nil {
		return err
	}
	_ = connection.Close()
	if s.startErr != nil {
		return s.startErr
	}
	s.started = true
	return nil
}
func (s *testSharedTelegramSource) Stop(context.Context) error { return nil }

type testShutdowner struct{ called chan struct{} }

func (s testShutdowner) Shutdown(...fx.ShutdownOption) error {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return nil
}

func TestSharedHTTPIngressUsesOnlySharedSocket(t *testing.T) {
	oldBackoffice := occupiedTestAddress(t)
	oldWebhooks := occupiedTestAddress(t)
	oldGateway := occupiedTestAddress(t)
	shared := freeTestAddress(t)
	params := sharedHTTPTestParams(t, shared)
	params.Config.Backoffice.ListenAddr = oldBackoffice
	params.Config.Webhooks.ListenAddr = oldWebhooks
	params.Config.Slack.Agent.ListenAddr = oldGateway

	server, err := startSharedHTTPIngress(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	for path, want := range map[string]int{
		"/balda/backoffice/login":        http.StatusOK,
		"/balda/gateway/slack/events":    http.StatusAccepted,
		"/old/slack/events":              http.StatusAccepted,
		"/balda/webhooks/orders":         http.StatusAccepted,
		"/balda/webhooks/unknown":        http.StatusNotFound,
		"/balda/gateway/webhooks/orders": http.StatusNotFound,
	} {
		response, err := http.Get("http://" + shared + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, response.StatusCode, want)
		}
	}
}

func TestSharedHTTPIngressRejectsBusyBindAndRouteConflict(t *testing.T) {
	shared := occupiedTestAddress(t)
	params := sharedHTTPTestParams(t, shared)
	if _, err := startSharedHTTPIngress(t.Context(), params); err == nil || !strings.Contains(err.Error(), shared) {
		t.Fatalf("busy shared bind error = %v", err)
	}

	available := freeTestAddress(t)
	params = sharedHTTPTestParams(t, available)
	params.GatewayCallbacks = []httpfx.GatewayCallbackProvider{func(context.Context) ([]httpfx.GatewayCallback, error) {
		return []httpfx.GatewayCallback{{Owner: "colliding gateway", Transport: "slack", Endpoint: "events",
			LegacyPath: "/balda/backoffice/login", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}, nil
	}}
	if _, err := startSharedHTTPIngress(t.Context(), params); err == nil || !strings.Contains(err.Error(), "colliding gateway") || !strings.Contains(err.Error(), "backoffice") {
		t.Fatalf("route conflict error = %v", err)
	}
	listener, err := net.Listen("tcp", available)
	if err != nil {
		t.Fatalf("conflict started shared socket: %v", err)
	}
	_ = listener.Close()
}

func TestSharedHTTPIngressRollsBackAfterLaterTransportFailure(t *testing.T) {
	shared := freeTestAddress(t)
	params := sharedHTTPTestParams(t, shared)
	var httpStage lifecycleStage
	for _, stage := range applicationLifecycleStages(params, &telegramLifecycle{}) {
		if stage.name == sharedHTTPIngressStage {
			httpStage = stage
		}
	}
	coordinator := newApplicationLifecycle(zerolog.Nop(), []lifecycleStage{httpStage, {
		name: "transport failure", start: func(context.Context) error { return errors.New("transport unavailable") },
	}})
	if err := coordinator.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "transport unavailable") {
		t.Fatalf("startup error = %v", err)
	}
	listener, err := net.Listen("tcp", shared)
	if err != nil {
		t.Fatalf("shared listener survived rollback: %v", err)
	}
	_ = listener.Close()
}

func TestTelegramRegistrationFollowsSharedBindAndUnwindsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "registration failed"}[fail], func(t *testing.T) {
			shared := freeTestAddress(t)
			params := sharedHTTPTestParams(t, shared)
			source := &testSharedTelegramSource{address: shared}
			if fail {
				source.startErr = errors.New("Telegram registration failed")
			}
			params.TelegramSource = source
			params.GatewayCallbacks = append(params.GatewayCallbacks, func(context.Context) ([]httpfx.GatewayCallback, error) {
				handler, legacyPath := source.HTTPCallback()
				return []httpfx.GatewayCallback{{Owner: "Telegram webhook", Transport: "telegram", Endpoint: "webhook", LegacyPath: legacyPath, Handler: handler}}, nil
			})
			runCalled := make(chan struct{}, 1)
			telegram := &telegramLifecycle{enabled: true, source: source, logger: zerolog.Nop(), run: func(ctx context.Context) error {
				runCalled <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}}
			var stages []lifecycleStage
			for _, stage := range applicationLifecycleStages(params, telegram) {
				if stage.name == sharedHTTPIngressStage || stage.name == "telegram ingress" {
					stages = append(stages, stage)
				}
			}
			coordinator := newApplicationLifecycle(zerolog.Nop(), stages)
			err := coordinator.Start(t.Context())
			if fail {
				if err == nil || !strings.Contains(err.Error(), "Telegram registration failed") {
					t.Fatalf("registration error = %v", err)
				}
				select {
				case <-runCalled:
					t.Fatal("bot ran after registration failed")
				default:
				}
			} else {
				if err != nil || !source.started {
					t.Fatalf("source started = %v, error = %v", source.started, err)
				}
				if err := coordinator.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", shared)
			if err != nil {
				t.Fatalf("shared listener survived shutdown: %v", err)
			}
			_ = listener.Close()
		})
	}
}

func TestUnexpectedTelegramRunFailureRequestsShutdown(t *testing.T) {
	shared := freeTestAddress(t)
	params := sharedHTTPTestParams(t, shared)
	source := &testSharedTelegramSource{address: shared}
	params.TelegramSource = source
	notification := make(chan struct{}, 1)
	telegram := &telegramLifecycle{enabled: true, source: source, logger: zerolog.Nop(), shutdowner: testShutdowner{called: notification},
		run: func(context.Context) error { return errors.New("bot loop failed") }}
	var stages []lifecycleStage
	for _, stage := range applicationLifecycleStages(params, telegram) {
		if stage.name == sharedHTTPIngressStage || stage.name == "telegram ingress" {
			stages = append(stages, stage)
		}
	}
	coordinator := newApplicationLifecycle(zerolog.Nop(), stages)
	if err := coordinator.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notification:
	case <-time.After(5 * time.Second):
		t.Fatal("unexpected bot exit did not request shutdown")
	}
	if err := coordinator.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", shared)
	if err != nil {
		t.Fatalf("shared listener survived shutdown: %v", err)
	}
	_ = listener.Close()
}

func sharedHTTPTestParams(t *testing.T, sharedAddress string) applicationLifecycleParams {
	return sharedHTTPTestParamsWithBasePath(t, sharedAddress, "/balda")
}

func sharedHTTPTestParamsWithBasePath(t *testing.T, sharedAddress, basePath string) applicationLifecycleParams {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	config := BaldaConfig{HTTP: HTTPConfig{ListenAddr: sharedAddress, BaseURL: "http://" + sharedAddress, BasePath: &basePath}}
	backofficeConfig, err := backofficeRuntimeConfig(config, state.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(backofficeConfig, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{
		Username: "superuser", Password: []byte("correct horse battery staple"),
	}); err != nil {
		t.Fatal(err)
	}
	registry, err := httpfx.NewRegistry(basePath)
	if err != nil {
		t.Fatal(err)
	}
	callback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	return applicationLifecycleParams{
		Config: config, HTTPConfig: ResolvedHTTPConfig{ListenAddr: sharedAddress, BaseURL: "http://" + sharedAddress, BasePath: basePath},
		Backoffice: runtime, HTTPRegistry: registry, StateProvider: provider,
		GatewayCallbacks: []httpfx.GatewayCallbackProvider{func(context.Context) ([]httpfx.GatewayCallback, error) {
			return []httpfx.GatewayCallback{{Owner: "slack events", Transport: "slack", Endpoint: "events", LegacyPath: "/old/slack/events", Handler: callback}}, nil
		}},
		WebhookHTTP: func(_ context.Context, routes *httpfx.Registry) error {
			return routes.SetWebhookLookup("generic webhooks", callback, func(_ context.Context, path string) (bool, error) {
				return path == "/balda/webhooks/orders", nil
			})
		},
	}
}

func occupiedTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String()
}

func freeTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
