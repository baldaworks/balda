// Package hookprovider owns provider-neutral authenticated bindings for native hooks.
package hookprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

const (
	maxNativeBytes       = 256 << 10
	maxNativeResultBytes = 256 << 10
)

// Runner executes normalized hook events for a binding.
type Runner interface {
	Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
}

type sessionReleaser interface {
	ReleaseSession(sessionID string)
}

// BindingScope is trusted context attached to every event received by a binding.
// Provider is opaque event data and never selects transport behavior.
type BindingScope struct {
	SessionID string
	Provider  string
	CWD       string
}

// Service owns private bindings, one loopback endpoint, and their lifetimes.
type Service struct {
	runner                Runner
	sessionLifecycleGate  sync.RWMutex
	sessionLocksMu        sync.Mutex
	sessionLocks          map[string]*sessionLock
	mu                    sync.Mutex
	server                *http.Server
	address               string
	bindings              map[bindingID]*boundSession
	sessionGenerations    map[string]sessionGeneration
	nextSessionGeneration uint64
	authDummy             [16]byte
	closed                bool
}

type sessionLock struct {
	mu    sync.Mutex
	users int
}

type sessionGeneration struct {
	id   uint64
	refs int
}

type boundSession struct {
	scope       BindingScope
	tokenSecret [16]byte
	ownerProof  string
	ownerPID    int
	ownerStart  string
	ctx         context.Context
	cancel      context.CancelFunc
	dir         string
	processes   map[int]string
	ready       chan struct{}
	readyOnce   sync.Once
	generation  uint64
}

type bindingID [16]byte

// Binding is an opaque authenticated capability for one native session.
type Binding struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	path       string
	ownerProof string
	release    func() error
}

// New creates a provider-neutral binding service.
func New(runner Runner) (*Service, error) {
	if runner == nil {
		return nil, hookcmd.ErrUnavailable
	}
	var authDummy [16]byte
	if _, err := rand.Read(authDummy[:]); err != nil {
		return nil, err
	}
	return &Service{
		runner:             runner,
		bindings:           make(map[bindingID]*boundSession),
		sessionGenerations: make(map[string]sessionGeneration),
		sessionLocks:       make(map[string]*sessionLock),
		authDummy:          authDummy,
	}, nil
}

func (s *Service) lockSession(sessionID string) *sessionLock {
	s.sessionLocksMu.Lock()
	lock := s.sessionLocks[sessionID]
	if lock == nil {
		lock = &sessionLock{}
		s.sessionLocks[sessionID] = lock
	}
	lock.users++
	s.sessionLocksMu.Unlock()
	lock.mu.Lock()
	return lock
}

func (s *Service) unlockSession(sessionID string, lock *sessionLock) {
	lock.mu.Unlock()
	s.sessionLocksMu.Lock()
	lock.users--
	if lock.users == 0 {
		delete(s.sessionLocks, sessionID)
	}
	s.sessionLocksMu.Unlock()
}

// Bind creates an isolated capability without retaining the caller's context.
// Release owns cancellation after Bind returns.
func (s *Service) Bind(ctx context.Context, scope BindingScope) (*Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.SessionID == "" || strings.TrimSpace(scope.CWD) == "" {
		return nil, hookcmd.ErrInvalid
	}
	cwd, err := filepath.Abs(scope.CWD)
	if err != nil {
		return nil, hookcmd.ErrInvalid
	}
	scope.CWD = cwd
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	var id bindingID
	copy(id[:], tokenBytes[:16])
	var secret [16]byte
	copy(secret[:], tokenBytes[16:])
	ownerProofBytes := make([]byte, 32)
	if _, err := rand.Read(ownerProofBytes); err != nil {
		return nil, err
	}
	ownerProof := hex.EncodeToString(ownerProofBytes)
	dir, err := os.MkdirTemp("", "balda-native-hooks-")
	if err != nil {
		return nil, err
	}
	s.sessionLifecycleGate.RLock()
	sessionLock := s.lockSession(scope.SessionID)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.unlockSession(scope.SessionID, sessionLock)
		s.sessionLifecycleGate.RUnlock()
		_ = os.RemoveAll(dir)
		return nil, hookcmd.ErrUnavailable
	}
	if s.server == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			s.mu.Unlock()
			s.unlockSession(scope.SessionID, sessionLock)
			s.sessionLifecycleGate.RUnlock()
			_ = os.RemoveAll(dir)
			return nil, err
		}
		s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
		s.address = "http://" + listener.Addr().String() + "/invoke"
		go func(server *http.Server) { _ = server.Serve(listener) }(s.server)
	}
	bindingCtx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(dir, "binding.json")
	generation := s.sessionGenerations[scope.SessionID]
	if generation.refs == 0 {
		s.nextSessionGeneration++
		generation.id = s.nextSessionGeneration
	}
	generation.refs++
	s.sessionGenerations[scope.SessionID] = generation
	bound := &boundSession{scope: scope, tokenSecret: secret, ownerProof: ownerProof, ctx: bindingCtx, cancel: cancel, dir: dir, processes: make(map[int]string), ready: make(chan struct{}), generation: generation.id}
	s.bindings[id] = bound
	binding := &Binding{URL: s.address, Token: token, path: path, ownerProof: ownerProof}
	data, err := json.Marshal(binding)
	s.mu.Unlock()
	s.unlockSession(scope.SessionID, sessionLock)
	s.sessionLifecycleGate.RUnlock()
	var once sync.Once
	binding.release = func() error {
		var releaseErr error
		once.Do(func() {
			s.sessionLifecycleGate.RLock()
			sessionLock := s.lockSession(scope.SessionID)
			s.mu.Lock()
			processes := maps.Clone(bound.processes)
			_, existed := s.bindings[id]
			if existed {
				delete(s.bindings, id)
			}
			generation, current := s.sessionGenerations[scope.SessionID]
			lastForSession := existed && current && generation.id == bound.generation
			if lastForSession {
				generation.refs--
				lastForSession = generation.refs == 0
				if lastForSession {
					delete(s.sessionGenerations, scope.SessionID)
				} else {
					s.sessionGenerations[scope.SessionID] = generation
				}
			}
			s.mu.Unlock()
			cancel()
			if releaser, ok := s.runner.(sessionReleaser); ok && lastForSession {
				releaser.ReleaseSession(scope.SessionID)
			}
			s.unlockSession(scope.SessionID, sessionLock)
			s.sessionLifecycleGate.RUnlock()
			terminateNativeProcesses(processes)
			releaseErr = os.RemoveAll(dir)
		})
		return releaseErr
	}
	if err == nil {
		err = os.WriteFile(path, data, 0600)
	}
	if err != nil {
		_ = binding.Release()
		return nil, err
	}
	return binding, nil
}

// Reference returns the private on-disk capability reference for a child process.
func (b *Binding) Reference() string { return b.path }

// OwnerProof returns the opaque argument that attests the binding's process owner.
// Callers choose where it appears; the service assigns no meaning to the
// surrounding command shape.
func (b *Binding) OwnerProof() string { return b.ownerProof }

// Release revokes the capability, cancels work, terminates registered descendants, and removes private files.
func (b *Binding) Release() error {
	if b == nil || b.release == nil {
		return nil
	}
	return b.release()
}

func (s *Service) authenticate(value string) (bindingID, *boundSession) {
	var zero bindingID
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return zero, nil
	}
	var id bindingID
	copy(id[:], decoded[:16])
	s.mu.Lock()
	binding := s.bindings[id]
	expected := s.authDummy
	if binding != nil {
		expected = binding.tokenSecret
	}
	s.mu.Unlock()
	matched := subtle.ConstantTimeCompare(decoded[16:], expected[:])
	if binding == nil || matched != 1 {
		return zero, nil
	}
	return id, binding
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
	key, binding := s.authenticate(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
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
		if err != nil || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || s.processMutation(key, binding, request) != nil {
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
	event.SessionID, event.Provider, event.CWD, event.Source = binding.scope.SessionID, binding.scope.Provider, binding.scope.CWD, "native"
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
	data, err = json.Marshal(result)
	if err != nil || len(data) > maxNativeResultBytes {
		http.Error(w, "hook execution unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(data, '\n'))
}

// Close invalidates every capability and stops the loopback endpoint.
func (s *Service) Close() error {
	s.sessionLifecycleGate.Lock()
	defer s.sessionLifecycleGate.Unlock()
	s.mu.Lock()
	s.closed = true
	server := s.server
	var dirs []string
	processes := make(map[int]string)
	sessions := make([]string, 0, len(s.sessionGenerations))
	for sessionID := range s.sessionGenerations {
		sessions = append(sessions, sessionID)
	}
	for key, binding := range s.bindings {
		dirs = append(dirs, binding.dir)
		maps.Copy(processes, binding.processes)
		binding.cancel()
		delete(s.bindings, key)
	}
	clear(s.sessionGenerations)
	s.mu.Unlock()
	if releaser, ok := s.runner.(sessionReleaser); ok {
		for _, sessionID := range sessions {
			releaser.ReleaseSession(sessionID)
		}
	}
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
