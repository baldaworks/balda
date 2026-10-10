package httpfx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
)

// Server owns one inbound HTTP socket. The caller starts it after its runtime
// and all area handlers are ready, then watches Done and Err for Serve failure.
type Server struct {
	listenAddr string
	handler    http.Handler

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	serveErr error
}

// NewServer constructs an inert HTTP server; Start performs the single bind.
func NewServer(listenAddr string, handler http.Handler) *Server {
	return &Server{listenAddr: listenAddr, handler: handler}
}

// Start binds the configured TCP address and launches the serving loop.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		if s.serveErr != nil {
			return s.serveErr
		}
		select {
		case <-s.done:
			return errors.New("shared HTTP server has stopped")
		default:
		}
		return nil
	}
	if s.handler == nil {
		return fmt.Errorf("shared HTTP handler is required")
	}
	if s.listenAddr == "" {
		return fmt.Errorf("shared HTTP listen address is required")
	}
	listener, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen for shared HTTP on %s: %w", s.listenAddr, err)
	}
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	s.server, s.listener, s.done, s.serveErr = server, listener, make(chan struct{}), nil
	go func() {
		err := server.Serve(listener)
		s.mu.Lock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.serveErr = fmt.Errorf("serve shared HTTP: %w", err)
		}
		close(s.done)
		s.mu.Unlock()
	}()
	return nil
}

// Addr returns the bound address after Start, or nil before a successful bind.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Done closes when Serve exits. It is nil until Start succeeds.
func (s *Server) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Err reports an unexpected serving error after Done closes.
func (s *Server) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

// Stop drains accepted connections and closes the listener on timeout.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	server, done := s.server, s.done
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		<-done
		return fmt.Errorf("shutdown shared HTTP: %w", err)
	}
	<-done
	return s.Err()
}
