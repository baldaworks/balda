package skill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type executorCall struct {
	selector Selector
	prompt   string
}

type recordingExecutor struct {
	calls []executorCall
	err   error
}

func (e *recordingExecutor) ExecuteSkill(_ context.Context, _ actorlayer.Envelope, _ commandcmd.Payload, selector Selector, prompt string) error {
	e.calls = append(e.calls, executorCall{selector: selector, prompt: prompt})
	return e.err
}

type recordingDispatcher struct {
	envelopes []actorlayer.Envelope
	err       error
}

func (d *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandlerPassesSelectorAndOptionalPrompt(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		wantSelector Selector
		wantPrompt   string
	}{
		{name: "unqualified without prompt", args: "review", wantSelector: Selector{Name: "review"}},
		{name: "unqualified with prompt", args: "review inspect this package", wantSelector: Selector{Name: "review"}, wantPrompt: "inspect this package"},
		{name: "plugin qualified with prompt", args: "prism:story implement this feature", wantSelector: Selector{Plugin: "prism", Name: "story"}, wantPrompt: "implement this feature"},
		{name: "surrounding whitespace", args: "  prism:story\t implement this  ", wantSelector: Selector{Plugin: "prism", Name: "story"}, wantPrompt: "implement this"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			dispatcher := &recordingDispatcher{}
			handler := New(executor, dispatcher, zerolog.Nop())

			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, testPayload(test.args)); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if len(executor.calls) != 1 {
				t.Fatalf("executor calls = %d, want 1", len(executor.calls))
			}
			if got := executor.calls[0]; got.selector != test.wantSelector || got.prompt != test.wantPrompt {
				t.Errorf("executor call = %+v, want selector %+v prompt %q", got, test.wantSelector, test.wantPrompt)
			}
			if len(dispatcher.envelopes) != 0 {
				t.Errorf("deliveries = %d, want 0", len(dispatcher.envelopes))
			}
		})
	}
}

func TestHandlerRejectsInvalidInvocationWithoutExecuting(t *testing.T) {
	tests := []string{"", "   ", ":story", "prism:", "a:b:c"}
	for _, args := range tests {
		t.Run(args, func(t *testing.T) {
			executor := &recordingExecutor{}
			dispatcher := &recordingDispatcher{}
			handler := New(executor, dispatcher, zerolog.Nop())
			payload := testPayload(args)
			payload.Invocation.Root = "/balda"

			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, payload); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if len(executor.calls) != 0 {
				t.Errorf("executor calls = %d, want 0", len(executor.calls))
			}
			if got := deliveredText(t, dispatcher); !strings.Contains(got, "/balda skill <skill> [prompt...]") || !strings.Contains(got, "/balda skill <plugin>:<skill> [prompt...]") {
				t.Errorf("usage = %q", got)
			}
		})
	}
}

func TestHandlerRequiresSessionCommandAccess(t *testing.T) {
	executor := &recordingExecutor{}
	dispatcher := &recordingDispatcher{}
	handler := New(executor, dispatcher, zerolog.Nop())
	payload := testPayload("review inspect this")
	payload.Access = commandcmd.Access{}

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(executor.calls) != 0 {
		t.Errorf("executor calls = %d, want 0", len(executor.calls))
	}
	if got := deliveredText(t, dispatcher); got != msgDenied {
		t.Errorf("delivery = %q, want %q", got, msgDenied)
	}
}

func TestHandlerMapsExpectedExecutionErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: ErrNotFound, want: msgNotFound},
		{name: "ambiguous", err: ErrAmbiguous, want: msgAmbiguous},
		{name: "revision unavailable", err: ErrRevisionUnavailable, want: msgRevisionUnavailable},
		{name: "runtime unavailable", err: ErrRuntimeUnavailable, want: msgRuntimeUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{err: test.err}
			dispatcher := &recordingDispatcher{}
			handler := New(executor, dispatcher, zerolog.Nop())

			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, testPayload("review")); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if got := deliveredText(t, dispatcher); got != test.want {
				t.Errorf("delivery = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHandlerReturnsUnexpectedExecutionFailureAsTransient(t *testing.T) {
	executionErr := errors.New("dispatch failed")
	handler := New(&recordingExecutor{err: executionErr}, &recordingDispatcher{}, zerolog.Nop())

	err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, testPayload("review"))
	if !errors.Is(err, executionErr) {
		t.Fatalf("Handle() error = %v, want wrapped execution error", err)
	}
	if got := actorlayer.ClassifyError(err); got != actorlayer.ErrorKindTransient {
		t.Errorf("error kind = %q, want %q", got, actorlayer.ErrorKindTransient)
	}
}

func TestHandlerReportsUnavailableExecutor(t *testing.T) {
	dispatcher := &recordingDispatcher{}
	handler := New(nil, dispatcher, zerolog.Nop())

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, testPayload("review")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := deliveredText(t, dispatcher); got != msgRuntimeUnavailable {
		t.Errorf("delivery = %q, want %q", got, msgRuntimeUnavailable)
	}
}

func TestHandlerReturnsResponseDeliveryFailureAsTransient(t *testing.T) {
	dispatchErr := errors.New("delivery unavailable")
	handler := New(&recordingExecutor{err: ErrNotFound}, &recordingDispatcher{err: dispatchErr}, zerolog.Nop())

	err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "command-1"}, testPayload("review"))
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("Handle() error = %v, want wrapped delivery error", err)
	}
	if got := actorlayer.ClassifyError(err); got != actorlayer.ErrorKindTransient {
		t.Errorf("error kind = %q, want %q", got, actorlayer.ErrorKindTransient)
	}
}

func testPayload(args string) commandcmd.Payload {
	return commandcmd.Payload{
		Name:       commandName,
		Args:       args,
		Locator:    deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "session-1"},
		Access:     commandcmd.Access{SessionCommands: true},
		Invocation: commandcmd.Invocation{Root: "/"},
	}
}

func deliveredText(t *testing.T, dispatcher *recordingDispatcher) string {
	t.Helper()
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(dispatcher.envelopes))
	}
	var payload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &payload); err != nil {
		t.Fatalf("UnmarshalPayload() error = %v", err)
	}
	return payload.Text
}
