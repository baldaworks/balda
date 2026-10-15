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
	OwnerPID  int    `json:"owner_pid"`
	ChildPID  int    `json:"child_pid"`
}

func (s *Service) processMutation(token bindingID, binding *boundSession, request processRequest) error {
	if request.ChildPID <= 1 || request.OwnerPID <= 1 {
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
	owner, err := readNativeProcess(request.OwnerPID)
	if err != nil || owner.Parent != os.Getpid() {
		return hookcmd.ErrForbidden
	}
	s.mu.Lock()
	boundOwnerPID, boundOwnerStart := binding.ownerPID, binding.ownerStart
	s.mu.Unlock()
	if boundOwnerPID == 0 {
		matched, err := processHasArgument(request.OwnerPID, binding.ownerProof)
		if err != nil || !matched {
			return hookcmd.ErrForbidden
		}
	} else if request.OwnerPID != boundOwnerPID || owner.Start != boundOwnerStart {
		return hookcmd.ErrForbidden
	}
	child, err := readNativeProcess(request.ChildPID)
	if err != nil || child.Parent != request.OwnerPID || child.Group != request.ChildPID {
		return hookcmd.ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings[token] != binding || len(binding.processes) >= 8 {
		return hookcmd.ErrUnavailable
	}
	if binding.ownerPID == 0 {
		binding.ownerPID, binding.ownerStart = request.OwnerPID, owner.Start
	} else if binding.ownerPID != request.OwnerPID || binding.ownerStart != owner.Start {
		return hookcmd.ErrForbidden
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

// RegisterProcess registers a direct child process for binding-owned cleanup.
func (b Binding) RegisterProcess(ctx context.Context, pid int) (func(), error) {
	request := processRequest{Operation: "attach", OwnerPID: os.Getpid(), ChildPID: pid}
	if err := sendProcessMutation(ctx, b, request); err != nil {
		return nil, err
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		request.Operation = "detach"
		_ = sendProcessMutation(cleanupCtx, b, request)
	}, nil
}

// UnregisterProcess removes a child after it has exited and can no longer be owned.
func (b Binding) UnregisterProcess(ctx context.Context, pid int) error {
	return sendProcessMutation(ctx, b, processRequest{Operation: "detach", OwnerPID: os.Getpid(), ChildPID: pid})
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
