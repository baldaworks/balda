package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// Runtime composes Backoffice operations over the selected Balda database only.
type Runtime struct {
	config          ResolvedConfig
	provider        state.Provider
	state           *StateService
	bootstrap       *BootstrapService
	owned           bool
	mu              sync.Mutex
	server          *http.Server
	done            chan struct{}
	serveErr        error
	invitations     BindingInvitations
	bindingChannels BindingChannels
}

// NewRuntime constructs Backoffice over a provider owned by its host.
func NewRuntime(config ResolvedConfig, provider state.Provider) (*Runtime, error) {
	if err := webui.VerifyAssets(); err != nil {
		return nil, fmt.Errorf("verify Backoffice frontend: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("backoffice state provider is required")
	}
	stateService, err := NewStateService(provider.Users())
	if err != nil {
		return nil, err
	}
	bootstrap, err := NewBootstrapService(provider.Users())
	if err != nil {
		return nil, err
	}
	return &Runtime{config: config, provider: provider, state: stateService, bootstrap: bootstrap}, nil
}

// OpenRuntime opens the selected backend for standalone maintenance operations.
func OpenRuntime(ctx context.Context, config ResolvedConfig) (*Runtime, error) {
	provider, err := state.Open(ctx, config.Database)
	if err != nil {
		return nil, fmt.Errorf("open Backoffice state: %w", err)
	}
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		_ = provider.Close()
		return nil, err
	}
	runtime.owned = true
	return runtime, nil
}

// Close releases a provider opened by OpenRuntime, but never a host provider.
func (r *Runtime) Close() error {
	if r == nil || !r.owned {
		return nil
	}
	return r.provider.Close()
}

// ValidateReady checks canonical administrator bootstrap state.
func (r *Runtime) ValidateReady(ctx context.Context) error {
	return r.state.ValidateReady(ctx)
}

// BootstrapAdmin establishes credentials after provider migrations have completed.
func (r *Runtime) BootstrapAdmin(ctx context.Context, input BootstrapInput) (BootstrapResult, error) {
	return r.bootstrap.Bootstrap(ctx, input)
}

// Recover2FA performs confirmed offline recovery without starting HTTP or ingress.
func (r *Runtime) Recover2FA(ctx context.Context, input RecoveryInput) (RecoveryResult, error) {
	service, err := NewRecoveryService(r.provider.Users())
	if err != nil {
		return RecoveryResult{}, err
	}
	return service.Recover(ctx, input)
}

// Start validates readiness and binds HTTP before returning to the host.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil {
		return nil
	}
	if err := r.ValidateReady(ctx); err != nil {
		return err
	}
	httpApplication, err := newHTTPApp(r.provider.Users(), r.config)
	if err != nil {
		return fmt.Errorf("construct Backoffice HTTP application: %w", err)
	}
	httpApplication.invitations = r.invitations
	httpApplication.bindingChannels = r.bindingChannels
	handler, err := httpApplication.handler()
	if err != nil {
		return fmt.Errorf("construct Backoffice HTTP routes: %w", err)
	}
	listener, err := net.Listen("tcp", r.config.Server.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for Backoffice: %w", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	r.server = server
	r.done = make(chan struct{})
	r.serveErr = nil
	go func() {
		err := server.Serve(listener)
		r.mu.Lock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.serveErr = fmt.Errorf("serve Backoffice: %w", err)
		}
		close(r.done)
		r.mu.Unlock()
	}()
	return nil
}

// Done closes when the HTTP serving loop exits; Err reports its failure.
func (r *Runtime) Done() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// Err returns a background serving error after Done closes.
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.serveErr
}

// Stop gracefully shuts down the listener without closing the shared provider.
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	server, done := r.server, r.done
	r.mu.Unlock()
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown Backoffice: %w", err)
	}
	<-done
	r.mu.Lock()
	r.server = nil
	err := r.serveErr
	r.mu.Unlock()
	return err
}

func healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	return mux
}

// ConfigureBindingInvitations wires host-owned services before the HTTP listener starts.
func (r *Runtime) ConfigureBindingInvitations(invitations BindingInvitations, channels BindingChannels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || invitations == nil || channels == nil {
		return fmt.Errorf("binding services must be configured before Backoffice start")
	}
	r.invitations = invitations
	r.bindingChannels = channels
	return nil
}
