package backoffice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
)

// ServeQA runs the synthetic Backoffice gallery on a loopback listener.
func ServeQA(ctx context.Context, listenAddr string, onReady func(string)) error {
	if ctx == nil {
		return fmt.Errorf("QA context is required")
	}
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil || !isLoopbackHost(host) {
		return fmt.Errorf("QA listener must use a loopback host:port address")
	}
	if err := webui.VerifyAssets(); err != nil {
		return fmt.Errorf("verify Backoffice frontend: %w", err)
	}
	handler, err := QAHandler("")
	if err != nil {
		return fmt.Errorf("construct Backoffice QA handler: %w", err)
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen for Backoffice QA: %w", err)
	}
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	done := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			shutdownDone <- server.Shutdown(shutdownCtx)
		case <-done:
			shutdownDone <- nil
		}
	}()
	if onReady != nil {
		onReady(listener.Addr().String())
	}
	err = server.Serve(listener)
	close(done)
	if shutdownErr := <-shutdownDone; shutdownErr != nil {
		return fmt.Errorf("stop Backoffice QA: %w", shutdownErr)
	}
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve Backoffice QA: %w", err)
	}
	return nil
}
