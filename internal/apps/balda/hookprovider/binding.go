package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// ReadBinding opens and validates a private binding reference.
func ReadBinding(path string) (Binding, error) {
	var binding Binding
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return binding, hookcmd.ErrUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &binding) != nil {
		return binding, hookcmd.ErrUnavailable
	}
	parsed, err := url.Parse(binding.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "/invoke" || parsed.User != nil || parsed.RawQuery != "" || len(binding.Token) != 64 {
		return Binding{}, hookcmd.ErrUnavailable
	}
	return binding, nil
}

// Invoke sends one normalized event through the binding.
func (b Binding) Invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	var result hookcmd.Result
	data, err := json.Marshal(event)
	if err != nil || len(data) > maxNativeBytes {
		return result, hookcmd.ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(data))
	if err != nil {
		return result, hookcmd.ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+b.Token)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return result, hookcmd.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	data, err = io.ReadAll(io.LimitReader(response.Body, maxNativeResultBytes+1))
	if err != nil || len(data) > maxNativeResultBytes || response.StatusCode != http.StatusOK {
		return result, hookcmd.ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return result, hookcmd.ErrUnavailable
	}
	return result, nil
}
