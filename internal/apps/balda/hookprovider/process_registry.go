package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

type nativeProcessInfo struct {
	Parent, Group int
	Start         string
}
type processRequest struct {
	Operation string `json:"operation"`
	ProxyPID  int    `json:"proxy_pid"`
	ChildPID  int    `json:"child_pid"`
}

func (s *Service) processMutation(token string, binding *boundSession, request processRequest) error {
	if request.ChildPID <= 1 || request.ProxyPID <= 1 {
		return hookcmd.ErrForbidden
	}
	if request.Operation == "detach" {
		current, err := readNativeProcess(request.ChildPID)
		s.mu.Lock()
		start := binding.processes[request.ChildPID]
		s.mu.Unlock()
		if err == nil && current.Start == start {
			return hookcmd.ErrForbidden
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(binding.processes, request.ChildPID)
		return nil
	}
	if request.Operation != "attach" {
		return hookcmd.ErrInvalid
	}
	proxy, err := readNativeProcess(request.ProxyPID)
	if err != nil || proxy.Parent != os.Getpid() {
		return hookcmd.ErrForbidden
	}
	matched, err := proxyOwnsBinding(request.ProxyPID, binding.path)
	if err != nil || !matched {
		return hookcmd.ErrForbidden
	}
	child, err := readNativeProcess(request.ChildPID)
	if err != nil || child.Parent != request.ProxyPID || child.Group != request.ChildPID {
		return hookcmd.ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings[token] != binding || len(binding.processes) >= 8 {
		return hookcmd.ErrUnavailable
	}
	binding.processes[request.ChildPID] = child.Start
	return nil
}

func terminateNativeProcesses(processes map[int]string) {
	for pid, start := range processes {
		current, err := readNativeProcess(pid)
		// An exited or reused PID cannot identify a process we own.
		if err != nil || current.Start != start || current.Group != pid {
			continue
		}
		_ = killNativeGroup(pid)
	}
}

func registerNativeProcess(ctx context.Context, binding Binding, pid int) (func(), error) {
	request := processRequest{Operation: "attach", ProxyPID: os.Getpid(), ChildPID: pid}
	if err := sendProcessMutation(ctx, binding, request); err != nil {
		return nil, err
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		request.Operation = "detach"
		_ = sendProcessMutation(cleanupCtx, binding, request)
	}, nil
}

func sendProcessMutation(ctx context.Context, binding Binding, payload processRequest) error {
	parsed, err := url.Parse(binding.URL)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	parsed.Path = processPath
	data, err := json.Marshal(payload)
	if err != nil {
		return hookcmd.ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(data))
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+binding.Token)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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

func bindingArgumentsMatch(args []string, path string) bool {
	for i := 0; i+3 < len(args); i++ {
		if args[i] == "agent-hooks" && args[i+1] == "proxy" && args[i+2] == path && args[i+3] == "--" {
			return true
		}
	}
	return false
}
