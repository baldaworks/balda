package app

import (
	"context"
	"reflect"
	"testing"
)

type lifecycleSpy struct {
	name  string
	log   *[]string
	start bool
}

func (s *lifecycleSpy) Start(context.Context) error {
	*s.log = append(*s.log, "start:"+s.name)
	s.start = true
	return nil
}

func (s *lifecycleSpy) Close(context.Context) error {
	*s.log = append(*s.log, "close:"+s.name)
	return nil
}

func TestRuntimeStartsAndClosesLifecycleInOrder(t *testing.T) {
	t.Parallel()

	var log []string
	first := &lifecycleSpy{name: "first", log: &log}
	second := &lifecycleSpy{name: "second", log: &log}
	runtime, err := NewRuntime(RuntimeConfig{Lifecycle: []Lifecycle{first, second}})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("idempotent Start() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close() error = %v", err)
	}
	want := []string{"start:first", "start:second", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %v, want %v", log, want)
	}
}
