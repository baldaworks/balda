package agent

import (
	"context"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	acpagent "github.com/normahq/go-adk-acpagent/v2"
	"github.com/rs/zerolog"
)

type permissionReviewerFunc func(context.Context, permissioncmd.Request) (permissioncmd.Decision, error)

func (f permissionReviewerFunc) Review(ctx context.Context, request permissioncmd.Request) (permissioncmd.Decision, error) {
	return f(ctx, request)
}

func TestPermissionHandlerTranslatesInteractionAndSelection(t *testing.T) {
	var got permissioncmd.Request
	reviewer := permissionReviewerFunc(func(_ context.Context, request permissioncmd.Request) (permissioncmd.Decision, error) {
		got = request
		return permissioncmd.Decision{OptionID: "reject"}, nil
	})
	handler := NewPermissionHandler(reviewer, zerolog.Nop())
	ctx := permissioncmd.WithInteraction(context.Background(), questioncmd.InteractionContext{SessionID: "session-1"})
	response, err := handler(ctx, acpagent.PermissionRequest{
		ToolCall: acpagent.PermissionToolCall{ID: "call-1", RawInput: map[string]any{"command": "pwd"}},
		Options: []acpagent.PermissionOption{
			{ID: "allow", Name: "Allow", Kind: acpagent.PermissionOptionKindAllowOnce},
			{ID: "reject", Name: "Reject", Kind: acpagent.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if got.Interaction.SessionID != "session-1" || got.ToolCall.ID != "call-1" {
		t.Fatalf("translated request = %+v", got)
	}
	if response.OptionID != "reject" {
		t.Fatalf("decision = %+v, want reject", response)
	}
}

func TestPermissionHandlerWithoutReviewerRejectsInsteadOfAllowingFirstOption(t *testing.T) {
	handler := NewPermissionHandler(nil, zerolog.Nop())
	response, err := handler(context.Background(), acpagent.PermissionRequest{Options: []acpagent.PermissionOption{
		{ID: "allow", Name: "Allow", Kind: acpagent.PermissionOptionKindAllowOnce},
		{ID: "reject", Name: "Reject", Kind: acpagent.PermissionOptionKindRejectOnce},
	}})
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if response.OptionID != "reject" {
		t.Fatalf("decision = %+v, want reject", response)
	}
}
