package slackagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// BindingAdmitter handles account credentials before conversational ingress.
type BindingAdmitter interface {
	Consume(ctx context.Context, proof usercmd.BindingProof) (string, error)
}

// SetBindingAdmitter installs the composition root's account admission adapter.
func (h *Server) SetBindingAdmitter(admitter BindingAdmitter, client *Client) {
	h.bindings = admitter
	h.bindingClient = client
}

// BindingIdentity is the bot identity verified by the configured Slack credential.
type BindingIdentity struct {
	TeamID string
	UserID string
	BotID  string
	Team   string
	User   string
}

// BindingIdentity verifies the configured bot token with Slack's auth.test API.
func (c *Client) BindingIdentity(ctx context.Context) (BindingIdentity, error) {
	var response struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
		Team   string `json:"team"`
		User   string `json:"user"`
	}
	if err := c.postJSON(ctx, "auth.test", struct{}{}, &response); err != nil {
		return BindingIdentity{}, err
	}
	if !response.OK {
		return BindingIdentity{}, &APIError{Method: "auth.test", StatusCode: 200, Code: response.Error, Message: response.Error, Retryable: retryableSlackCode(response.Error)}
	}
	if strings.TrimSpace(response.TeamID) == "" || strings.TrimSpace(response.UserID) == "" || strings.TrimSpace(response.BotID) == "" {
		return BindingIdentity{}, fmt.Errorf("slack bot identity is unavailable")
	}
	return BindingIdentity{TeamID: response.TeamID, UserID: response.UserID, BotID: response.BotID, Team: response.Team, User: response.User}, nil
}

func (h *Server) consumeBinding(ctx context.Context, proof usercmd.BindingProof) string {
	if h.bindings == nil {
		return "Account binding is unavailable. Open Backoffice Access and try again."
	}
	if _, err := h.bindings.Consume(ctx, proof); err != nil {
		return "Account binding failed. Use the complete current invitation in this bot's direct conversation, or generate a new invitation in Backoffice Access."
	}
	return "Account connected. You can now use Balda from this conversation."
}
