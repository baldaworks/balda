package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

func TestClientLauncherAuthorizationChallenge(t *testing.T) {
	for _, transport := range []string{transportStreamableHTTP, "sse"} {
		for _, bridged := range []bool{false, true} {
			name := transport + "/direct"
			if bridged {
				name = transport + "/bridge"
			}
			t.Run(name, func(t *testing.T) {
				cases := []struct {
					name, header string
					status       int
					want         mcpruntime.FailureReason
				}{
					{"declared metadata", `Bearer resource_metadata="ORIGIN/.well-known/oauth-protected-resource/mcp"`, 401, mcpruntime.FailureAuthorizationChallenge},
					{"foreign metadata", `Bearer resource_metadata="https://foreign.example/metadata"`, 401, mcpruntime.FailureUnavailable},
					{"missing metadata", `Bearer realm="private fixture secret"`, 401, mcpruntime.FailureUnavailable},
					{"malformed", `Bearer resource_metadata="unterminated`, 401, mcpruntime.FailureUnavailable},
					{"permission denied", `Bearer resource_metadata="ORIGIN/.well-known/oauth-protected-resource/mcp"`, 403, mcpruntime.FailureUnavailable},
					{"bare unauthorized", "", 401, mcpruntime.FailureUnavailable},
				}
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						var origin string
						var calls atomic.Int32
						var discover, initialize atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							if r.Method == http.MethodPost {
								var message struct {
									Method string `json:"method"`
								}
								if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
									t.Error(err)
								}
								switch message.Method {
								case "server/discover":
									discover.Add(1)
								case "initialize":
									initialize.Add(1)
								default:
									t.Errorf("unsolicited RPC: %s", message.Method)
								}
							}
							if r.URL.Path != "/mcp" {
								t.Errorf("unsolicited discovery at %s", r.URL.Path)
							}
							w.Header().Set("WWW-Authenticate", strings.ReplaceAll(tc.header, "ORIGIN", origin))
							w.WriteHeader(tc.status)
						}))
						defer server.Close()
						origin = server.URL
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						config := mcpruntime.LaunchConfig{Transport: transport, URL: origin + "/mcp"}
						if bridged {
							bridge := mcpbridge.New(nil, nil)
							if err := bridge.Start(ctx); err != nil {
								t.Fatal(err)
							}
							defer func() { _ = bridge.Close(context.Background()) }()
							config.Headers = map[string]string{"X-Fixture": "private fixture secret"}
							var err error
							config, err = BridgeLaunch(bridge, "fixture", config, nil, nil)
							if err != nil {
								t.Fatal(err)
							}
						}
						_, err := NewClientLauncher().Start(ctx, mcpruntime.InstanceKey{}, config)
						var failure *mcpruntime.LaunchError
						if !errors.As(err, &failure) || failure.Reason != tc.want {
							t.Fatalf("Start = %v, want safe reason %s", err, tc.want)
						}
						if strings.Contains(err.Error(), "private fixture secret") || strings.Contains(err.Error(), origin) {
							t.Fatalf("private response escaped: %v", err)
						}
						wantCalls := int32(1)
						if transport == transportStreamableHTTP {
							// SDK1.7 tries discover then initialize; neither may replay.
							wantCalls = 2
							if discover.Load() != 1 || initialize.Load() != 1 {
								t.Errorf("discovery/initialize = %d/%d, want one each", discover.Load(), initialize.Load())
							}
						}
						if calls.Load() != wantCalls {
							t.Errorf("upstream requests = %d, want %d without replay/metadata fetch", calls.Load(), wantCalls)
						}
					})
				}
			})
		}
	}
}

func TestClientLauncherChallengeDoesNotMaskMalformedInitialize(t *testing.T) {
	for _, bridged := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "bridge"}[bridged], func(t *testing.T) {
			var origin string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request.Method == "server/discover" {
					w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+origin+`/.well-known/oauth-protected-resource"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`not-json`))
			}))
			defer upstream.Close()
			origin = upstream.URL
			config := mcpruntime.LaunchConfig{Transport: transportStreamableHTTP, URL: origin + "/mcp"}
			if bridged {
				bridge := mcpbridge.New(nil, nil)
				if err := bridge.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = bridge.Close(context.Background()) }()
				config.Headers = map[string]string{"X-Worker": "fixture"}
				var err error
				config, err = BridgeLaunch(bridge, "fixture", config, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err := NewClientLauncher().Start(ctx, mcpruntime.InstanceKey{}, config)
			var failure *mcpruntime.LaunchError
			if !errors.As(err, &failure) || failure.Reason != mcpruntime.FailureUnavailable {
				t.Fatalf("malformed initialize classified as %v; want fatal unavailable", err)
			}
		})
	}
}
