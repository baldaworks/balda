package balda

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestApplicationLifecycleStartsInOrderAndStopsInReverse(t *testing.T) {
	t.Parallel()

	var calls []string
	stage := func(name string) lifecycleStage {
		return lifecycleStage{
			name: name,
			start: func(context.Context) error {
				calls = append(calls, "start "+name)
				return nil
			},
			stop: func(context.Context) error {
				calls = append(calls, "stop "+name)
				return nil
			},
		}
	}
	lifecycle := newApplicationLifecycle(zerolog.Nop(), []lifecycleStage{
		stage("mcp"),
		stage("provider"),
		stage("actors"),
		stage("ingress"),
	})

	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	want := []string{
		"start mcp",
		"start provider",
		"start actors",
		"start ingress",
		"stop ingress",
		"stop actors",
		"stop provider",
		"stop mcp",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lifecycle calls = %v, want %v", calls, want)
	}
}

func TestApplicationLifecycleStagesStartQuestionProjectorAfterTransport(t *testing.T) {
	t.Parallel()

	stages := applicationLifecycleStages(applicationLifecycleParams{
		TransportStages: []appports.TransportLifecycleStage{
			{Name: "zulip ingress"},
			{Name: "slack agent ingress"},
		},
	}, &telegramLifecycle{})
	names := make([]string, 0, len(stages))
	for _, stage := range stages {
		names = append(names, stage.name)
	}

	want := []string{
		"user readiness",
		"bundled MCP",
		"runtime contribution catalog",
		"session-memory runtime",
		"provider runtime",
		"session manager",
		"durable transport",
		"session-memory ingress outbox",
		"session memory",
		"turn dispatcher",
		"question delivery binding projector",
		"job event projector",
		"job event outbox",
		"actor host",
		"scheduled jobs",
		"Backoffice HTTP",
		"inbound webhooks",
		"zulip ingress",
		"slack agent ingress",
		"telegram ingress",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("lifecycle stages = %v, want %v", names, want)
	}
}

func TestBackofficeReadinessAndRollbackBeforeIngress(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: backoffice.ResolvedServerConfig{
		ListenAddr: address, PublicURL: "http://127.0.0.1:8095",
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour,
	}}, provider)
	if err != nil {
		t.Fatal(err)
	}
	ingressStarted := false
	coordinator := newApplicationLifecycle(zerolog.Nop(), []lifecycleStage{
		{name: "user readiness", start: runtime.ValidateReady},
		{name: "Backoffice HTTP", start: runtime.Start, stop: runtime.Stop},
		{name: "ingress", start: func(context.Context) error {
			ingressStarted = true
			return errors.New("ingress failed")
		}},
	})
	if err := coordinator.Start(t.Context()); err == nil || ingressStarted {
		t.Fatalf("startup without admin = %v, ingress started = %t", err, ingressStarted)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{
		Username: "admin", Password: []byte("correct horse battery staple"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(t.Context()); err == nil || !ingressStarted {
		t.Fatalf("later ingress failure = %v, ingress started = %t", err, ingressStarted)
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatal("Backoffice listener remained open after ingress rollback")
	}
}

func TestApplicationLifecycleRollsBackStartedStages(t *testing.T) {
	t.Parallel()

	startErr := errors.New("listen failed")
	var calls []string
	lifecycle := newApplicationLifecycle(zerolog.Nop(), []lifecycleStage{
		{
			name: "provider",
			start: func(context.Context) error {
				calls = append(calls, "start provider")
				return nil
			},
			stop: func(context.Context) error {
				calls = append(calls, "stop provider")
				return nil
			},
		},
		{
			name: "ingress",
			start: func(context.Context) error {
				calls = append(calls, "start ingress")
				return startErr
			},
		},
	})

	err := lifecycle.Start(context.Background())
	if !errors.Is(err, startErr) {
		t.Fatalf("Start() error = %v, want %v", err, startErr)
	}
	want := []string{"start provider", "start ingress", "stop provider"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lifecycle calls = %v, want %v", calls, want)
	}
}
