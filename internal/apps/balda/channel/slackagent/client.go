package slackagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultAPIBaseURL        = "https://slack.com/api"
	defaultHTTPClientTimeout = 15 * time.Second
	maxResponseBodyBytes     = 1 << 20
	maxSessionTitleRunes     = 200
	maxStreamTextRunes       = 12000
)

type SessionStatus string

const (
	SessionStatusActive     SessionStatus = "active"
	SessionStatusProcessing SessionStatus = "processing"
	SessionStatusSuspended  SessionStatus = "suspended"
	SessionStatusClosed     SessionStatus = "closed"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type postMessageRequest struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Mrkdwn   bool   `json:"mrkdwn"`
}

type SetSessionStatusRequest struct {
	ChannelID       string
	ThreadTS        string
	Status          SessionStatus
	Title           string
	InitiatorUserID string
}

type setSessionStatusRequest struct {
	ChannelID       string        `json:"channel_id"`
	ThreadTS        string        `json:"thread_ts"`
	Status          SessionStatus `json:"status"`
	Title           string        `json:"title,omitempty"`
	InitiatorUserID string        `json:"initiator_user_id,omitempty"`
}

type renameSessionRequest struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts"`
	Title     string `json:"title"`
}

type startStreamRequest struct {
	Channel      string `json:"channel"`
	ThreadTS     string `json:"thread_ts"`
	MarkdownText string `json:"markdown_text,omitempty"`
}

type streamUpdateRequest struct {
	Channel       string        `json:"channel"`
	TS            string        `json:"ts"`
	MarkdownText  string        `json:"markdown_text,omitempty"`
	SessionStatus SessionStatus `json:"session_status,omitempty"`
}

type slackResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Warning string `json:"warning"`
	TS      string `json:"ts"`
}

type APIError struct {
	Method     string
	StatusCode int
	Code       string
	Message    string
	RetryAfter time.Duration
	Retryable  bool
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("slack %s returned HTTP %d (%s): %s", e.Method, e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("slack %s returned HTTP %d: %s", e.Method, e.StatusCode, e.Message)
}

func IsRetryableSlackError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func NewClient(token string) *Client {
	return NewClientWithBaseURL(defaultAPIBaseURL, token)
}

func NewClientWithBaseURL(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: defaultHTTPClientTimeout},
	}
}

func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string, mrkdwn bool) (string, error) {
	req := postMessageRequest{
		Channel:  strings.TrimSpace(channel),
		ThreadTS: strings.TrimSpace(threadTS),
		Text:     strings.TrimSpace(text),
		Mrkdwn:   mrkdwn,
	}
	if req.Channel == "" {
		return "", fmt.Errorf("slack channel is required")
	}
	if req.Text == "" {
		return "", fmt.Errorf("slack message text is required")
	}
	return c.callWithTS(ctx, "chat.postMessage", req)
}

func (c *Client) SetSessionStatus(ctx context.Context, input SetSessionStatusRequest) error {
	req := setSessionStatusRequest{
		ChannelID:       strings.TrimSpace(input.ChannelID),
		ThreadTS:        strings.TrimSpace(input.ThreadTS),
		Status:          input.Status,
		Title:           strings.TrimSpace(input.Title),
		InitiatorUserID: strings.TrimSpace(input.InitiatorUserID),
	}
	if err := validateSessionTarget(req.ChannelID, req.ThreadTS); err != nil {
		return err
	}
	if !validSessionStatus(req.Status) {
		return fmt.Errorf("invalid slack agent session status %q", req.Status)
	}
	if req.Title != "" && utf8.RuneCountInString(req.Title) > maxSessionTitleRunes {
		return fmt.Errorf("slack agent session title exceeds %d characters", maxSessionTitleRunes)
	}
	return c.call(ctx, "agents.sessions.setStatus", req)
}

func (c *Client) RenameSession(ctx context.Context, channelID, threadTS, title string) error {
	req := renameSessionRequest{
		ChannelID: strings.TrimSpace(channelID),
		ThreadTS:  strings.TrimSpace(threadTS),
		Title:     strings.TrimSpace(title),
	}
	if err := validateSessionTarget(req.ChannelID, req.ThreadTS); err != nil {
		return err
	}
	if count := utf8.RuneCountInString(req.Title); count < 1 || count > maxSessionTitleRunes {
		return fmt.Errorf("slack agent session title must contain 1-%d characters", maxSessionTitleRunes)
	}
	return c.call(ctx, "agents.sessions.rename", req)
}

func (c *Client) StartStream(ctx context.Context, channel, threadTS, markdownText string) (string, error) {
	req := startStreamRequest{
		Channel:      strings.TrimSpace(channel),
		ThreadTS:     strings.TrimSpace(threadTS),
		MarkdownText: markdownText,
	}
	if err := validateSessionTarget(req.Channel, req.ThreadTS); err != nil {
		return "", err
	}
	if utf8.RuneCountInString(req.MarkdownText) > maxStreamTextRunes {
		return "", fmt.Errorf("slack stream text exceeds %d characters", maxStreamTextRunes)
	}
	return c.callWithTS(ctx, "chat.startStream", req)
}

func (c *Client) AppendStream(ctx context.Context, channel, ts, markdownText string) error {
	req := streamUpdateRequest{
		Channel:      strings.TrimSpace(channel),
		TS:           strings.TrimSpace(ts),
		MarkdownText: markdownText,
	}
	if err := validateStreamTarget(req.Channel, req.TS); err != nil {
		return err
	}
	if strings.TrimSpace(req.MarkdownText) == "" {
		return fmt.Errorf("slack stream text is required")
	}
	if utf8.RuneCountInString(req.MarkdownText) > maxStreamTextRunes {
		return fmt.Errorf("slack stream text exceeds %d characters", maxStreamTextRunes)
	}
	return c.call(ctx, "chat.appendStream", req)
}

func (c *Client) StopStream(ctx context.Context, channel, ts, markdownText string, status SessionStatus) error {
	req := streamUpdateRequest{
		Channel:       strings.TrimSpace(channel),
		TS:            strings.TrimSpace(ts),
		MarkdownText:  markdownText,
		SessionStatus: status,
	}
	if err := validateStreamTarget(req.Channel, req.TS); err != nil {
		return err
	}
	if req.SessionStatus != "" && !validSessionStatus(req.SessionStatus) {
		return fmt.Errorf("invalid slack agent session status %q", req.SessionStatus)
	}
	if utf8.RuneCountInString(req.MarkdownText) > maxStreamTextRunes {
		return fmt.Errorf("slack stream text exceeds %d characters", maxStreamTextRunes)
	}
	return c.call(ctx, "chat.stopStream", req)
}

func (c *Client) call(ctx context.Context, method string, payload any) error {
	_, err := c.callSlack(ctx, method, payload, false)
	return err
}

func (c *Client) callWithTS(ctx context.Context, method string, payload any) (string, error) {
	response, err := c.callSlack(ctx, method, payload, true)
	if err != nil {
		return "", err
	}
	return response.TS, nil
}

func (c *Client) callSlack(ctx context.Context, method string, payload any, requireTS bool) (slackResponse, error) {
	var response slackResponse
	if err := c.postJSON(ctx, method, payload, &response); err != nil {
		return slackResponse{}, err
	}
	if !response.OK {
		code := strings.TrimSpace(response.Error)
		return slackResponse{}, &APIError{Method: method, StatusCode: http.StatusOK, Code: code, Message: code, Retryable: retryableSlackCode(code)}
	}
	response.TS = strings.TrimSpace(response.TS)
	if requireTS && response.TS == "" {
		return slackResponse{}, &APIError{Method: method, StatusCode: http.StatusOK, Code: "malformed_response", Message: "missing ts"}
	}
	return response, nil
}

func (c *Client) postJSON(ctx context.Context, method string, payload any, out any) error {
	if c == nil {
		return fmt.Errorf("slack client is required")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return fmt.Errorf("slack api base url is required")
	}
	if strings.TrimSpace(c.token) == "" {
		return fmt.Errorf("slack bot token is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode slack %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack %s request: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	httpClient := c.http
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPClientTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s request: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := readLimitedResponseBody(resp.Body)
	if err != nil {
		return fmt.Errorf("read slack %s response body: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &APIError{
			Method:     method,
			StatusCode: resp.StatusCode,
			Message:    http.StatusText(resp.StatusCode),
			RetryAfter: retryAfter,
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &APIError{Method: method, StatusCode: resp.StatusCode, Code: "malformed_response", Message: "invalid JSON response", Retryable: true}
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, method string, params url.Values, out any) error {
	if c == nil {
		return fmt.Errorf("slack client is required")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return fmt.Errorf("slack api base url is required")
	}
	if strings.TrimSpace(c.token) == "" {
		return fmt.Errorf("slack bot token is required")
	}
	endpoint := c.baseURL + "/" + method
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build slack %s request: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	httpClient := c.http
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPClientTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s request: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readLimitedResponseBody(resp.Body)
	if err != nil {
		return fmt.Errorf("read slack %s response body: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Method:     method,
			StatusCode: resp.StatusCode,
			Message:    http.StatusText(resp.StatusCode),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &APIError{Method: method, StatusCode: resp.StatusCode, Code: "malformed_response", Message: "invalid JSON response", Retryable: true}
	}
	return nil
}

func validateSessionTarget(channel, threadTS string) error {
	if strings.TrimSpace(channel) == "" {
		return fmt.Errorf("slack channel is required")
	}
	if strings.TrimSpace(threadTS) == "" {
		return fmt.Errorf("slack thread timestamp is required")
	}
	return nil
}

func validateStreamTarget(channel, ts string) error {
	if strings.TrimSpace(channel) == "" {
		return fmt.Errorf("slack channel is required")
	}
	if strings.TrimSpace(ts) == "" {
		return fmt.Errorf("slack stream timestamp is required")
	}
	return nil
}

func validSessionStatus(status SessionStatus) bool {
	switch status {
	case SessionStatusActive, SessionStatusProcessing, SessionStatusSuspended, SessionStatusClosed:
		return true
	default:
		return false
	}
}

func retryableSlackCode(code string) bool {
	switch strings.TrimSpace(code) {
	case "fatal_error", "internal_error", "ratelimited", "request_timeout", "service_unavailable", "team_added_to_org":
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func readLimitedResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBodyBytes {
		return nil, fmt.Errorf("slack response body too large: limit %d bytes", maxResponseBodyBytes)
	}
	return data, nil
}
