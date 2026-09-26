package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout       = 20 * time.Second
	maxResponseBodyBytes     = 1 << 20
	maxErrorResponseBodyText = 4096
)

// Client is a low-level Mattermost REST API v4 client.
//
// Auth is a bot access token presented as a bearer credential. Mattermost bot
// tokens are long-lived and scoped to the bot account.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// userID caches the local bot user identity resolved at construction time
	// by callers that need it; it is not fetched lazily here so that a bad
	// token fails loudly during startup rather than on first delivery.
	userID string
}

// APIError describes a Mattermost API response that rejected the request.
type APIError struct {
	Path       string
	StatusCode int
	ID         string
	Message    string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Sprintf("mattermost %s returned HTTP %d: %s", e.Path, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("mattermost %s returned HTTP %d (%s): %s", e.Path, e.StatusCode, e.ID, e.Message)
}

// NewClient creates a new Mattermost API client.
func NewClient(baseURL, token string, userID string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		userID:  strings.TrimSpace(userID),
		http:    &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// UserID returns the configured bot user ID, if any.
func (c *Client) UserID() string {
	if c == nil {
		return ""
	}
	return c.userID
}

// ValidateConfig validates the REST credentials needed to send Mattermost replies.
func ValidateConfig(serverURL, token string) error {
	trimmedURL := strings.TrimSpace(serverURL)
	if trimmedURL == "" {
		return fmt.Errorf("balda.mattermost.server_url is required when Mattermost is enabled")
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("balda.mattermost.server_url must be an absolute http(s) URL")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("balda.mattermost.server_url must use http or https")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("balda.mattermost.token is required when Mattermost is enabled")
	}
	return nil
}

// WebSocketURL derives the websocket endpoint used for inbound event streaming.
func (c *Client) WebSocketURL() (string, error) {
	if c == nil {
		return "", fmt.Errorf("mattermost client is required")
	}
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse mattermost server url: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("mattermost server url must use http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v4/websocket"
	return parsed.String(), nil
}

// APIBaseURL returns the REST base including the version prefix.
func (c *Client) APIBaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL + "/api/v4"
}

// Post is the Mattermost post resource (subset used by Balda).
type Post struct {
	ID        string `json:"id"`
	CreateAt  int64  `json:"create_at"`
	UpdateAt  int64  `json:"update_at"`
	DeleteAt  int64  `json:"delete_at"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
	Type      string `json:"type"`
}

// Channel is the Mattermost channel resource (subset used by Balda).
type Channel struct {
	ID          string `json:"id"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at"`
	TeamID      string `json:"team_id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	Name        string `json:"name"`
	Header      string `json:"header"`
	Purpose     string `json:"purpose"`
}

// User is the Mattermost user resource (subset used by Balda).
type User struct {
	ID       string `json:"id"`
	CreateAt int64  `json:"create_at"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
	Bot      bool   `json:"is_bot"`
	DeleteAt int64  `json:"delete_at"`
}

// FileInfo is the Mattermost file resource metadata (subset used by Balda).
type FileInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type"`
	PostID    string `json:"post_id"`
}

// channelMember is the Mattermost channel membership resource (subset).
type channelMember struct {
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
}

// CreatePost creates a channel post. An empty rootID posts at channel level;
// a non-empty rootID creates a reply inside that thread.
func (c *Client) CreatePost(ctx context.Context, channelID, rootID, message string) (Post, error) {
	if strings.TrimSpace(channelID) == "" {
		return Post{}, fmt.Errorf("mattermost create post requires channel_id")
	}
	if strings.TrimSpace(message) == "" {
		return Post{}, fmt.Errorf("mattermost create post requires message")
	}
	body := map[string]string{
		"channel_id": strings.TrimSpace(channelID),
		"message":    message,
	}
	if root := strings.TrimSpace(rootID); root != "" {
		body["root_id"] = root
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Post{}, fmt.Errorf("encode mattermost post: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "/posts", bytes.NewReader(raw), "application/json")
	if err != nil {
		return Post{}, err
	}
	var post Post
	if err := json.Unmarshal(response, &post); err != nil {
		return Post{}, fmt.Errorf("decode mattermost create post response: %w", err)
	}
	if strings.TrimSpace(post.ID) == "" {
		return Post{}, &APIError{
			Path:       "/posts",
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost create post response missing id",
		}
	}
	return post, nil
}

// UpdatePost edits an existing post in place. Mattermost supports this only for
// the post author within the configured edit window.
func (c *Client) UpdatePost(ctx context.Context, postID, message string) (Post, error) {
	if strings.TrimSpace(postID) == "" {
		return Post{}, fmt.Errorf("mattermost update post requires post_id")
	}
	raw, err := json.Marshal(map[string]string{"id": strings.TrimSpace(postID), "message": message})
	if err != nil {
		return Post{}, fmt.Errorf("encode mattermost post update: %w", err)
	}
	path := "/posts/" + url.PathEscape(strings.TrimSpace(postID))
	response, err := c.do(ctx, http.MethodPut, path, bytes.NewReader(raw), "application/json")
	if err != nil {
		return Post{}, err
	}
	var post Post
	if err := json.Unmarshal(response, &post); err != nil {
		return Post{}, fmt.Errorf("decode mattermost update post response: %w", err)
	}
	return post, nil
}

// GetPost fetches one post by ID.
func (c *Client) GetPost(ctx context.Context, postID string) (Post, error) {
	if strings.TrimSpace(postID) == "" {
		return Post{}, fmt.Errorf("mattermost get post requires post_id")
	}
	path := "/posts/" + url.PathEscape(strings.TrimSpace(postID))
	response, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return Post{}, err
	}
	var post Post
	if err := json.Unmarshal(response, &post); err != nil {
		return Post{}, fmt.Errorf("decode mattermost get post response: %w", err)
	}
	return post, nil
}

// GetChannel fetches one channel by ID.
func (c *Client) GetChannel(ctx context.Context, channelID string) (Channel, error) {
	if strings.TrimSpace(channelID) == "" {
		return Channel{}, fmt.Errorf("mattermost get channel requires channel_id")
	}
	path := "/channels/" + url.PathEscape(strings.TrimSpace(channelID))
	response, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return Channel{}, err
	}
	var channel Channel
	if err := json.Unmarshal(response, &channel); err != nil {
		return Channel{}, fmt.Errorf("decode mattermost get channel response: %w", err)
	}
	return channel, nil
}

// GetUser fetches one user by ID.
func (c *Client) GetUser(ctx context.Context, userID string) (User, error) {
	if strings.TrimSpace(userID) == "" {
		return User{}, fmt.Errorf("mattermost get user requires user_id")
	}
	path := "/users/" + url.PathEscape(strings.TrimSpace(userID))
	response, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return User{}, err
	}
	var user User
	if err := json.Unmarshal(response, &user); err != nil {
		return User{}, fmt.Errorf("decode mattermost get user response: %w", err)
	}
	return user, nil
}

// GetMe resolves the identity behind the configured token.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	response, err := c.do(ctx, http.MethodGet, "/users/me", nil, "")
	if err != nil {
		return User{}, err
	}
	var user User
	if err := json.Unmarshal(response, &user); err != nil {
		return User{}, fmt.Errorf("decode mattermost get me response: %w", err)
	}
	if strings.TrimSpace(user.ID) == "" {
		return User{}, &APIError{
			Path:       "/users/me",
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost get me response missing id",
		}
	}
	return user, nil
}

// GetTeamByName resolves a team by its URL slug.
func (c *Client) GetTeamByName(ctx context.Context, name string) (teamRef, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return teamRef{}, fmt.Errorf("mattermost get team requires name")
	}
	path := "/teams/name/" + url.PathEscape(trimmed)
	response, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return teamRef{}, err
	}
	var team teamRef
	if err := json.Unmarshal(response, &team); err != nil {
		return teamRef{}, fmt.Errorf("decode mattermost get team response: %w", err)
	}
	if strings.TrimSpace(team.ID) == "" {
		return teamRef{}, &APIError{
			Path:       path,
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost team response missing id",
		}
	}
	return team, nil
}

// GetChannelByName resolves a channel within a team by its URL slug.
func (c *Client) GetChannelByName(ctx context.Context, teamID, name string) (Channel, error) {
	if strings.TrimSpace(teamID) == "" {
		return Channel{}, fmt.Errorf("mattermost get channel by name requires team_id")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Channel{}, fmt.Errorf("mattermost get channel by name requires name")
	}
	path := "/teams/" + url.PathEscape(strings.TrimSpace(teamID)) + "/channels/name/" + url.PathEscape(trimmed)
	response, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return Channel{}, err
	}
	var channel Channel
	if err := json.Unmarshal(response, &channel); err != nil {
		return Channel{}, fmt.Errorf("decode mattermost get channel by name response: %w", err)
	}
	if strings.TrimSpace(channel.ID) == "" {
		return Channel{}, &APIError{
			Path:       path,
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost channel response missing id",
		}
	}
	return channel, nil
}

// GetDirectChannel resolves (creating when needed) the direct-message channel
// between the bot and one user. Mattermost requires exactly two user IDs.
func (c *Client) GetDirectChannel(ctx context.Context, botUserID, otherUserID string) (Channel, error) {
	first := strings.TrimSpace(botUserID)
	second := strings.TrimSpace(otherUserID)
	if first == "" || second == "" {
		return Channel{}, fmt.Errorf("mattermost direct channel requires two user ids")
	}
	raw, err := json.Marshal([]string{first, second})
	if err != nil {
		return Channel{}, fmt.Errorf("encode mattermost direct channel request: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "/channels/direct", bytes.NewReader(raw), "application/json")
	if err != nil {
		return Channel{}, err
	}
	var channel Channel
	if err := json.Unmarshal(response, &channel); err != nil {
		return Channel{}, fmt.Errorf("decode mattermost direct channel response: %w", err)
	}
	if strings.TrimSpace(channel.ID) == "" {
		return Channel{}, &APIError{
			Path:       "/channels/direct",
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost direct channel response missing id",
		}
	}
	return channel, nil
}

// UploadFile uploads one file into a channel, returning file metadata to be
// referenced by a subsequent CreatePost via file_ids.
func (c *Client) UploadFile(ctx context.Context, channelID, filename string, content io.Reader) (FileInfo, error) {
	if strings.TrimSpace(channelID) == "" {
		return FileInfo{}, fmt.Errorf("mattermost upload file requires channel_id")
	}
	if strings.TrimSpace(filename) == "" {
		return FileInfo{}, fmt.Errorf("mattermost upload file requires filename")
	}
	if content == nil {
		return FileInfo{}, fmt.Errorf("mattermost upload file requires content")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		return FileInfo{}, fmt.Errorf("build mattermost multipart file part: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return FileInfo{}, fmt.Errorf("write mattermost multipart file body: %w", err)
	}
	if err := writer.WriteField("channel_id", strings.TrimSpace(channelID)); err != nil {
		return FileInfo{}, fmt.Errorf("write mattermost multipart channel_id: %w", err)
	}
	if err := writer.Close(); err != nil {
		return FileInfo{}, fmt.Errorf("close mattermost multipart writer: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "/files", &body, writer.FormDataContentType())
	if err != nil {
		return FileInfo{}, err
	}
	var envelope struct {
		FileInfos []FileInfo `json:"file_infos"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return FileInfo{}, fmt.Errorf("decode mattermost upload file response: %w", err)
	}
	if len(envelope.FileInfos) == 0 || strings.TrimSpace(envelope.FileInfos[0].ID) == "" {
		return FileInfo{}, &APIError{
			Path:       "/files",
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost upload file response missing file infos",
		}
	}
	return envelope.FileInfos[0], nil
}

// CreatePostWithFiles creates a post carrying already-uploaded file IDs.
func (c *Client) CreatePostWithFiles(ctx context.Context, channelID, rootID, message string, fileIDs []string) (Post, error) {
	if strings.TrimSpace(channelID) == "" {
		return Post{}, fmt.Errorf("mattermost create post requires channel_id")
	}
	if len(fileIDs) == 0 {
		return c.CreatePost(ctx, channelID, rootID, message)
	}
	body := map[string]any{
		"channel_id": strings.TrimSpace(channelID),
		"message":    message,
		"file_ids":   fileIDs,
	}
	if root := strings.TrimSpace(rootID); root != "" {
		body["root_id"] = root
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Post{}, fmt.Errorf("encode mattermost post with files: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "/posts", bytes.NewReader(raw), "application/json")
	if err != nil {
		return Post{}, err
	}
	var post Post
	if err := json.Unmarshal(response, &post); err != nil {
		return Post{}, fmt.Errorf("decode mattermost create post with files response: %w", err)
	}
	if strings.TrimSpace(post.ID) == "" {
		return Post{}, &APIError{
			Path:       "/posts",
			StatusCode: http.StatusOK,
			ID:         "MALFORMED_RESPONSE",
			Message:    "mattermost create post with files response missing id",
		}
	}
	return post, nil
}

// AddChannelMember adds a user to a channel. Used to guarantee the bot is a
// participant before posting into a channel discovered from inbound traffic.
func (c *Client) AddChannelMember(ctx context.Context, channelID, userID string) error {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("mattermost add channel member requires channel_id and user_id")
	}
	raw, err := json.Marshal(channelMember{
		ChannelID: strings.TrimSpace(channelID),
		UserID:    strings.TrimSpace(userID),
	})
	if err != nil {
		return fmt.Errorf("encode mattermost channel member: %w", err)
	}
	path := "/channels/" + url.PathEscape(strings.TrimSpace(channelID)) + "/members"
	_, err = c.do(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json")
	return err
}

// Ping reports whether the Mattermost API is reachable and healthy.
func (c *Client) Ping(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodGet, "/system/ping", nil, "")
	if err != nil {
		return err
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response, &payload); err != nil {
		return fmt.Errorf("decode mattermost ping response: %w", err)
	}
	if !strings.EqualFold(payload.Status, "OK") {
		return fmt.Errorf("mattermost ping status %q", payload.Status)
	}
	return nil
}

type teamRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (c *Client) do(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	contentType string,
) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("mattermost client is required")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("mattermost server url is required")
	}
	if strings.TrimSpace(c.token) == "" {
		return nil, fmt.Errorf("mattermost token is required")
	}
	endpoint := c.APIBaseURL() + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build mattermost request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mattermost request to %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	response, err := readLimitedResponseBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mattermost response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErrorFromResponse(path, resp.StatusCode, response)
	}
	return response, nil
}

func apiErrorFromResponse(path string, statusCode int, body []byte) *APIError {
	var payload struct {
		ID         string `json:"id"`
		Message    string `json:"message"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.Unmarshal(body, &payload); err == nil &&
		(strings.TrimSpace(payload.Message) != "" || strings.TrimSpace(payload.ID) != "") {
		return &APIError{
			Path:       path,
			StatusCode: statusCode,
			ID:         payload.ID,
			Message:    payload.Message,
		}
	}
	return &APIError{
		Path:       path,
		StatusCode: statusCode,
		Message:    responseBodySnippet(body),
	}
}

// isContentRejectedError reports whether an API error is a content-level
// rejection (bad Markdown, bad attachment reference) rather than an
// infrastructure failure. Only these are retried with a plain-text fallback.
func isContentRejectedError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusBadRequest {
		return true
	}
	return apiErr.StatusCode == http.StatusOK &&
		strings.EqualFold(strings.TrimSpace(apiErr.ID), "BAD_REQUEST")
}

func readLimitedResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBodyBytes {
		return nil, fmt.Errorf("mattermost response body too large: limit %d bytes", maxResponseBodyBytes)
	}
	return data, nil
}

func responseBodySnippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) <= maxErrorResponseBodyText {
		return text
	}
	return text[:maxErrorResponseBodyText] + "...(truncated)"
}
