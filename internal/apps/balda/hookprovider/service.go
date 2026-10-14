// Package hookprovider adapts synchronous native tool interception to Balda hooks.
package hookprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/normahq/runtime/v2/agentconfig"
)

const (
	maxNativeBytes = 256 << 10
	kindClaude     = "claude"
	kindCodex      = "codex"
	kindOpenCode   = "opencode"
)

// Runner is the native adapter's consumer-owned execution port.
type Runner interface {
	Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
}

// Configuration is the adapter's read-only live policy port.
type Configuration interface {
	Load(ctx context.Context) (hookcmd.Record, error)
}

// Service owns private bindings, a loopback endpoint and their session lifetimes.
type Service struct {
	runner     Runner
	records    Configuration
	root       string
	providers  map[string]agentconfig.Config
	executable string
	mu         sync.Mutex
	server     *http.Server
	address    string
	bindings   map[string]*boundSession
	closed     bool
}

type boundSession struct {
	event     hookcmd.Event
	ctx       context.Context
	cancel    context.CancelFunc
	dir       string
	path      string
	processes map[int]string
	ready     chan struct{}
	readyOnce sync.Once
}

// Binding is the private file read by a provider's child dispatcher.
type Binding struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	Kind  string `json:"kind"`
}

// New keeps provider defaults immutable and defers the endpoint until a session.
func New(runner Runner, records Configuration, root string, providers map[string]agentconfig.Config) (*Service, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if runner == nil || records == nil {
		return nil, hookcmd.ErrUnavailable
	}
	return &Service{runner: runner, records: records, root: root, providers: maps.Clone(providers), executable: executable, bindings: make(map[string]*boundSession)}, nil
}

func providerKind(kind string) string {
	switch kind {
	case agentconfig.AgentTypeClaudeCodeACP, agentconfig.AgentTypeClaudeACP:
		return kindClaude
	case agentconfig.AgentTypeCodexACP:
		return kindCodex
	case agentconfig.AgentTypeOpenCodeACP:
		return kindOpenCode
	default:
		return ""
	}
}

func leaves(providers map[string]agentconfig.Config, root, executable string) ([]string, error) {
	var ids []string
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return hookcmd.ErrUnsupported
		}
		if visited[id] {
			return nil
		}
		config, ok := providers[id]
		if !ok {
			return hookcmd.ErrUnsupported
		}
		resolved, err := agentconfig.NormalizeConfig(config, executable)
		if err != nil {
			return hookcmd.ErrUnsupported
		}
		visiting[id] = true
		if agentconfig.IsPoolType(config.Type) {
			for _, member := range resolved.PoolMembers {
				if err := visit(member); err != nil {
					return err
				}
			}
		} else {
			ids = append(ids, id)
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}

// ValidateDefinition rejects tool interception that the configured runtime cannot provide.
func (s *Service) ValidateDefinition(_ context.Context, d hookcmd.Definition) error {
	if !d.Event.ToolEvent() || !d.Enabled {
		return nil
	}
	if !nativePlatformSupported() {
		return hookcmd.ErrUnsupported
	}
	if d.Provider != "" && d.Provider != s.root {
		return hookcmd.ErrUnsupported
	}
	ids, err := leaves(s.providers, s.root, s.executable)
	if err != nil || len(ids) == 0 {
		return hookcmd.ErrUnsupported
	}
	for _, id := range ids {
		if providerKind(s.providers[id].Type) == "" {
			return hookcmd.ErrUnsupported
		}
	}
	return nil
}

// Prepare installs session-owned commands without modifying provider or workspace config.
func (s *Service) Prepare(ctx context.Context, sessionID, providerID, cwd string, providers map[string]agentconfig.Config) (map[string]agentconfig.Config, func() error, error) {
	if sessionID == "" || strings.TrimSpace(cwd) == "" {
		return nil, nil, hookcmd.ErrInvalid
	}
	var err error
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, nil, hookcmd.ErrInvalid
	}
	ids, err := leaves(providers, providerID, s.executable)
	if err != nil {
		return nil, nil, err
	}
	record, err := s.records.Load(ctx)
	if err != nil || record.Validate() != nil {
		return nil, nil, hookcmd.ErrUnavailable
	}
	nativeRequired := false
	for _, d := range record.Definitions {
		if !d.Enabled || !d.Event.ToolEvent() || d.Provider != "" && d.Provider != providerID {
			continue
		}
		nativeRequired = true
		for _, id := range ids {
			if providerKind(providers[id].Type) == "" {
				return nil, nil, hookcmd.ErrUnsupported
			}
		}
	}
	if !nativeRequired {
		return maps.Clone(providers), func() error { return nil }, nil
	}
	if !nativePlatformSupported() {
		return nil, nil, hookcmd.ErrUnsupported
	}
	copyProviders := maps.Clone(providers)
	var releases []func() error
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			for _, releaseBinding := range releases {
				releaseErr = errors.Join(releaseErr, releaseBinding())
			}
		})
		return releaseErr
	}
	for _, id := range ids {
		config := providers[id]
		kind := providerKind(config.Type)
		if kind == "" {
			continue
		}
		resolved, err := agentconfig.NormalizeConfig(config, s.executable)
		if err != nil {
			_ = release()
			return nil, nil, err
		}
		bindingPath, releaseBinding, err := s.bind(kind, hookcmd.Event{SessionID: sessionID, Provider: providerID, CWD: cwd, Source: "native"})
		if err != nil {
			_ = release()
			return nil, nil, err
		}
		releases = append(releases, releaseBinding)
		config.Type = agentconfig.AgentTypeGenericACP
		config.GenericACP = &agentconfig.ACPConfig{Cmd: append([]string{s.executable, "agent-hooks", "proxy", bindingPath, "--"}, resolved.Command...), Model: resolved.Model, ModelConfigID: resolved.ModelConfigID, ReasoningEffort: resolved.ReasoningEffort, ReasoningEffortConfigID: resolved.ReasoningEffortConfigID, Mode: resolved.Mode}
		config.ClaudeCodeACP, config.CodexACP, config.OpenCodeACP = nil, nil, nil
		copyProviders[id] = config
	}
	return copyProviders, release, nil
}

func (s *Service) bind(kind string, event hookcmd.Event) (string, func() error, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	dir, err := os.MkdirTemp("", "balda-native-hooks-")
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
		return "", nil, hookcmd.ErrUnavailable
	}
	if s.server == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			s.mu.Unlock()
			_ = os.RemoveAll(dir)
			return "", nil, err
		}
		s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
		s.address = "http://" + listener.Addr().String() + "/invoke"
		go func(server *http.Server) { _ = server.Serve(listener) }(s.server)
	}
	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(dir, "binding.json")
	bound := &boundSession{event: event, ctx: ctx, cancel: cancel, dir: dir, path: path, processes: make(map[int]string), ready: make(chan struct{})}
	s.bindings[token] = bound
	data, err := json.Marshal(Binding{URL: s.address, Token: token, Kind: kind})
	s.mu.Unlock()
	var once sync.Once
	release := func() error {
		var err error
		once.Do(func() {
			s.mu.Lock()
			processes := maps.Clone(bound.processes)
			delete(s.bindings, token)
			s.mu.Unlock()
			cancel()
			terminateNativeProcesses(processes)
			err = os.RemoveAll(dir)
		})
		return err
	}
	if err == nil {
		err = os.WriteFile(path, data, 0600)
	}
	if err != nil {
		_ = release()
		return "", nil, err
	}
	return path, release, nil
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || r.URL.Path != "/invoke" && r.URL.Path != processPath && r.URL.Path != readyPath && r.URL.Path != readyWaitPath || r.Header.Get("Origin") != "" {
		http.Error(w, "unavailable", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "unavailable", http.StatusForbidden)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	binding := s.bindings[token]
	s.mu.Unlock()
	if binding == nil {
		http.Error(w, "unavailable", http.StatusForbidden)
		return
	}
	if r.URL.Path == readyPath {
		binding.readyOnce.Do(func() { close(binding.ready) })
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == readyWaitPath {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		select {
		case <-binding.ready:
			w.WriteHeader(http.StatusNoContent)
		case <-binding.ctx.Done():
			http.Error(w, "unavailable", http.StatusForbidden)
		case <-ctx.Done():
			http.Error(w, "pending", http.StatusServiceUnavailable)
		}
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNativeBytes))
	if r.URL.Path == processPath {
		var request processRequest
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err != nil || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || s.processMutation(token, binding, request) != nil {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var event hookcmd.Event
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err != nil || decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF || !event.Name.ToolEvent() {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	event.SessionID, event.Provider, event.CWD, event.Source = binding.event.SessionID, binding.event.Provider, binding.event.CWD, binding.event.Source
	ctx, cancel := context.WithCancel(binding.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	result, err := s.runner.Run(ctx, event)
	if err != nil {
		var denied *hookcmd.DeniedError
		if errors.As(err, &denied) {
			result = hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: denied.Reason}
		} else {
			http.Error(w, "hook execution unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return
	}
}

// Close invalidates capabilities and cancels hook commands before stopping ingress.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	server := s.server
	var dirs []string
	processes := make(map[int]string)
	for token, binding := range s.bindings {
		dirs = append(dirs, binding.dir)
		maps.Copy(processes, binding.processes)
		binding.cancel()
		delete(s.bindings, token)
	}
	s.mu.Unlock()
	terminateNativeProcesses(processes)
	var closeErr error
	for _, dir := range dirs {
		closeErr = errors.Join(closeErr, os.RemoveAll(dir))
	}
	if server != nil {
		return errors.Join(closeErr, server.Close())
	}
	return closeErr
}

// CapabilityDescription explains configuration support without probing credentials.
func (s *Service) CapabilityDescription() string {
	if s.ValidateDefinition(context.Background(), hookcmd.Definition{Enabled: true, Event: hookcmd.PreToolUse}) != nil {
		return "Native tool hooks are unavailable for the configured provider. Lifecycle hooks remain available."
	}
	return fmt.Sprintf("Native PreToolUse and PostToolUse adapters are configured for provider %s. Activation requires a new session: use /reset for an existing session. In-flight tool calls are unaffected. Unsupported native versions fail explicitly.", s.root)
}

// VerifySession blocks new turns in runtimes created before native activation.
func (s *Service) VerifySession(ctx context.Context, sessionID string) error {
	record, err := s.records.Load(ctx)
	if err != nil || record.Validate() != nil {
		return hookcmd.ErrUnavailable
	}
	required := false
	for _, d := range record.Definitions {
		if d.Enabled && d.Event.ToolEvent() && (d.Provider == "" || d.Provider == s.root) {
			required = true
			break
		}
	}
	if !required {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		if binding.event.SessionID == sessionID {
			return nil
		}
	}
	return hookcmd.ErrSessionReset
}
