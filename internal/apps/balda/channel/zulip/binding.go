package zulip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// BindingIdentity contains the configured realm and verified bot account.
type BindingIdentity struct {
	RealmURL string
	UserID   int
	Email    string
	FullName string
}

// BindingIdentity resolves the configured API credential's own bot profile.
func (c *Client) BindingIdentity(ctx context.Context) (BindingIdentity, error) {
	const path = "/api/v1/users/me"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return BindingIdentity{}, fmt.Errorf("build Zulip identity request: %w", err)
	}
	request.SetBasicAuth(c.botEmail, c.apiKey)
	response, err := c.http.Do(request)
	if err != nil {
		return BindingIdentity{}, fmt.Errorf("verify Zulip identity: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readLimitedResponseBody(response.Body)
	if err != nil {
		return BindingIdentity{}, err
	}
	if response.StatusCode != http.StatusOK {
		return BindingIdentity{}, zulipAPIErrorFromResponse(path, response.StatusCode, body)
	}
	var profile struct {
		Result   string `json:"result"`
		UserID   int    `json:"user_id"`
		Email    string `json:"email"`
		FullName string `json:"full_name"`
		IsBot    bool   `json:"is_bot"`
		IsActive bool   `json:"is_active"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		return BindingIdentity{}, fmt.Errorf("decode Zulip identity: %w", err)
	}
	if profile.Result != apiResultSuccess || profile.UserID <= 0 || !profile.IsBot || !profile.IsActive || !strings.EqualFold(profile.Email, c.botEmail) {
		return BindingIdentity{}, fmt.Errorf("zulip bot identity is unavailable")
	}
	return BindingIdentity{RealmURL: c.baseURL, UserID: profile.UserID, Email: profile.Email, FullName: profile.FullName}, nil
}
