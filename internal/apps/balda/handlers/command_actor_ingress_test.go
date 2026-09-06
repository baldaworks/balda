package handlers

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
)

type recordingCommandIngress struct{ requests []commandcmd.Request }

func (r *recordingCommandIngress) PublishCommand(_ context.Context, req commandcmd.Request) error {
	r.requests = append(r.requests, req)
	return nil
}

func TestCommandHandlerPublishesActorOwnedCommands(t *testing.T) {
	for _, name := range []string{"locator", "reset"} {
		t.Run(name, func(t *testing.T) {
			handler, sessions, _, _ := newCommandHandlerTestHarness(t)
			ingress := &recordingCommandIngress{}
			handler.commandIngress = ingress
			event := newCommandEvent(name, "", 101, 9001, nil)
			event.Message.MessageId = 77

			if err := handler.onCommand(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if len(ingress.requests) != 1 {
				t.Fatalf("published requests = %d, want 1", len(ingress.requests))
			}
			got := ingress.requests[0]
			if got.InvocationID != "telegram:command:9001:77" || got.Payload.Name != name || !got.Payload.Access.Owner {
				t.Fatalf("published request = %+v", got)
			}
			if len(sessions.resetCalls) != 0 || len(sessions.createCalls) != 0 {
				t.Fatalf("ingress executed session policy: reset=%d create=%d", len(sessions.resetCalls), len(sessions.createCalls))
			}
		})
	}
}
