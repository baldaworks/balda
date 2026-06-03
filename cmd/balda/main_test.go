package main

import (
	"context"
	"errors"
	"testing"
)

type testExitCoder struct {
	code int
}

func (e testExitCoder) Error() string {
	return "exit"
}

func (e testExitCoder) ExitCode() int {
	return e.code
}

func TestRunExpectedShutdownError(t *testing.T) {
	originalExecute := executeFn
	t.Cleanup(func() {
		executeFn = originalExecute
	})

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "context canceled",
			err:  context.Canceled,
		},
		{
			name: "wrapped expected cancel",
			err:  &unprintedCLIError{err: context.Canceled},
		},
		{
			name: "string classified cancel",
			err:  errors.New("context canceled"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executeFn = func() error {
				return tc.err
			}

			if got := run(); got != 0 {
				t.Fatalf("run() exit code = %d, want 0", got)
			}
		})
	}
}

func TestRunUnexpectedError(t *testing.T) {
	originalExecute := executeFn
	t.Cleanup(func() {
		executeFn = originalExecute
	})

	executeFn = func() error {
		return errors.New("boom")
	}

	if got := run(); got != 1 {
		t.Fatalf("run() exit code = %d, want 1", got)
	}
}

func TestRunExitCoder(t *testing.T) {
	originalExecute := executeFn
	t.Cleanup(func() {
		executeFn = originalExecute
	})

	executeFn = func() error {
		return testExitCoder{code: 7}
	}

	if got := run(); got != 7 {
		t.Fatalf("run() exit code = %d, want 7", got)
	}
}
