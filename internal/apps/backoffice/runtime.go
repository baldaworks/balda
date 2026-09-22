package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usermigration"
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
	config    ResolvedConfig
	provider  state.Provider
	state     *StateService
	bootstrap *BootstrapService
}

// OpenRuntime opens exactly the resolved Balda backend and constructs Backoffice use cases.
func OpenRuntime(ctx context.Context, config ResolvedConfig) (*Runtime, error) {
	if err := webui.VerifyAssets(); err != nil {
		return nil, fmt.Errorf("verify Backoffice frontend: %w", err)
	}
	provider, err := state.Open(ctx, config.Database)
	if err != nil {
		return nil, fmt.Errorf("open Backoffice state: %w", err)
	}
	stateService, err := NewStateService(provider.AppKV(), provider.Collaborators(), provider.Users())
	if err != nil {
		_ = provider.Close()
		return nil, err
	}
	bootstrap, err := NewBootstrapService(provider.Users())
	if err != nil {
		_ = provider.Close()
		return nil, err
	}
	return &Runtime{config: config, provider: provider, state: stateService, bootstrap: bootstrap}, nil
}

// Close releases the selected state backend.
func (r *Runtime) Close() error {
	if r == nil || r.provider == nil {
		return nil
	}
	return r.provider.Close()
}

// ValidateReady checks migration and administrator bootstrap state.
func (r *Runtime) ValidateReady(ctx context.Context) error {
	return r.state.ValidateReady(ctx)
}

// MigrateUsers runs the explicit forward-only legacy migration.
func (r *Runtime) MigrateUsers(ctx context.Context, outputPath, primarySubject string) (usermigration.Result, error) {
	return r.state.MigrateUsers(ctx, outputPath, primarySubject)
}

// BootstrapAdmin establishes credentials only after legacy migration requirements are satisfied.
func (r *Runtime) BootstrapAdmin(ctx context.Context, input BootstrapInput) (BootstrapResult, error) {
	if err := r.state.RequireMigrationComplete(ctx); err != nil {
		return BootstrapResult{}, err
	}
	return r.bootstrap.Bootstrap(ctx, input)
}

// Serve validates state and runs the Backoffice HTTP lifecycle without channel runtimes.
func (r *Runtime) Serve(ctx context.Context) error {
	if err := r.ValidateReady(ctx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", r.config.Server.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for Backoffice: %w", err)
	}
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler:           healthHandler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Backoffice: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown Backoffice: %w", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("stop Backoffice: %w", err)
		}
		return nil
	}
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
