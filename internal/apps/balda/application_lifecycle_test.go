package balda

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
