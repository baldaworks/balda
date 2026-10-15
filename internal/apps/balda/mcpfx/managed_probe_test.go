package mcpfx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuthProbeRequiresWorkerBindingBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "anonymous-fixture", Version: "1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer upstream.Close()
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := NewManagedProbe(credentials, NewClientLauncher(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = probe.ProbeMCP(t.Context(), mcpcmd.Revision{ConnectionID: "worker", ID: "unbound", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL, OAuth: true}})
	if !errors.Is(err, mcpcmd.ErrAuthRequired) || requests.Load() != 0 {
		t.Fatal("OAuth probe authenticated anonymously before a trusted binding existed")
	}
}

type probeLaunchCapture struct{ config chan mcpruntime.LaunchConfig }

func (c probeLaunchCapture) Start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Instance, error) {
	c.config <- config
	return NewClientLauncher().Start(ctx, key, config)
}

func TestProbeRetiresPrivateRouteWhenShutdownStalls(t *testing.T) {
	peer := mcp.NewServer(&mcp.Implementation{Name: "shutdown", Version: "1"}, nil)
	normal := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return peer }, nil)
	release, deleting, drained := make(chan struct{}), make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			close(deleting)
			defer close(drained)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		normal.ServeHTTP(w, r)
	}))
	defer func() { close(release); upstream.Close() }()
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(context.Background()) }()
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan mcpruntime.LaunchConfig, 1)
	probe, err := NewManagedProbe(credentials, probeLaunchCapture{config: captured}, bridge)
	if err != nil {
		t.Fatal(err)
	}
	probe.timeout = 150 * time.Millisecond
	_, err = probe.ProbeMCP(t.Context(), mcpcmd.Revision{ConnectionID: "worker", ID: "candidate", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL, Headers: map[string]mcpcmd.ValueBinding{"X-Worker": {Kind: mcpcmd.ValueLiteral, Value: "private"}}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe shutdown deadline = %v", err)
	}
	select {
	case <-deleting:
	case <-time.After(time.Second):
		t.Fatal("fixture did not reach shutdown DELETE")
	}
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("probe abandoned shutdown request and private route")
	}
	config := <-captured
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, config.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("retired probe route status = %d", response.StatusCode)
	}
}

func TestProbeDeadlineIncludesLaterDiscoveryPages(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "own deadline", true: "caller cancellation"}[cancelCaller], func(t *testing.T) {
			peer := mcp.NewServer(&mcp.Implementation{Name: "paged", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
			for _, name := range []string{"first", "second"} {
				mcp.AddTool(peer, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
					return &mcp.CallToolResult{}, nil, nil
				})
			}
			normal := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return peer }, nil)
			release, secondPage := make(chan struct{}), make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var message struct {
						Method string
						Params struct{ Cursor string }
					}
					_ = json.Unmarshal(body, &message)
					if message.Method == "tools/list" && message.Params.Cursor != "" {
						secondPage <- struct{}{}
						select {
						case <-release:
						case <-r.Context().Done():
						}
						return
					}
					if message.Method == "notifications/cancelled" {
						select {
						case <-release:
						case <-r.Context().Done():
						}
						w.WriteHeader(http.StatusAccepted)
						return
					}
				}
				normal.ServeHTTP(w, r)
			}))
			defer func() { close(release); upstream.Close() }()
			credentials, err := mcpmanage.New("")
			if err != nil {
				t.Fatal(err)
			}
			probe, err := NewManagedProbe(credentials, NewClientLauncher(), nil)
			if err != nil {
				t.Fatal(err)
			}
			probe.timeout = 150 * time.Millisecond
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelCaller {
				go func() {
					select {
					case <-secondPage:
						cancel()
					case <-caller.Done():
					}
				}()
			}
			started := time.Now()
			_, err = probe.ProbeMCP(caller, mcpcmd.Revision{ConnectionID: "worker", ID: "candidate", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL}})
			want := context.DeadlineExceeded
			if cancelCaller {
				want = context.Canceled
			} else {
				select {
				case <-secondPage:
				default:
					t.Fatal("fixture never reached later discovery page")
				}
			}
			if !errors.Is(err, want) || time.Since(started) > 500*time.Millisecond {
				t.Fatalf("later-page probe returned %v after %v", err, time.Since(started))
			}
		})
	}
}

func TestProbeBoundsHungDiscoveryIncludingCancellationCleanup(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer func() { close(release); upstream.Close() }()
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := NewManagedProbe(credentials, NewClientLauncher(), nil)
	if err != nil {
		t.Fatal(err)
	}
	probe.timeout = 35 * time.Millisecond
	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, err = probe.ProbeMCP(caller, mcpcmd.Revision{ConnectionID: "worker", ID: "candidate", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL}})
	if time.Since(started) > 200*time.Millisecond {
		t.Errorf("probe exceeded caller budget: %v", time.Since(started))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("probe deadline = %v", err)
	}
	if caller.Err() != nil {
		t.Fatal("probe consumed the outer caller deadline")
	}
}
