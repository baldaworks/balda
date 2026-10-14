package hookprovider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

const (
	readyPath     = "/ready"
	readyWaitPath = "/ready/wait"
	processPath   = "/process"
)

func sendReady(ctx context.Context, binding Binding, wait bool) error {
	parsed, err := url.Parse(binding.URL)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	parsed.Path = readyPath
	if wait {
		parsed.Path = readyWaitPath
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), nil)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+binding.Token)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 16 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return hookcmd.ErrUnavailable
	}
	return nil
}

func waitReady(parent context.Context, binding Binding) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if sendReady(ctx, binding, true) != nil {
		return hookcmd.ErrUnsupported
	}
	return nil
}
