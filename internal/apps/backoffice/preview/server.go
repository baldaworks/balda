package preview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/browseredge"
	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const (
	qaReadHeaderTimeout = 5 * time.Second
	qaReadTimeout       = 15 * time.Second
	qaWriteTimeout      = 30 * time.Second
	qaIdleTimeout       = 60 * time.Second
	qaShutdownTimeout   = 10 * time.Second
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
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen for Backoffice QA: %w", err)
	}
	defer func() { _ = listener.Close() }()
	origin := "http://" + listener.Addr().String()
	preview, err := browseredge.New(previewService{}, browseredge.Config{PublicOrigin: origin, MountPath: "/qa/ui"})
	if err != nil {
		return fmt.Errorf("construct Backoffice QA handler: %w", err)
	}
	server := &http.Server{
		Handler:           preview.Handler(),
		ReadHeaderTimeout: qaReadHeaderTimeout,
		ReadTimeout:       qaReadTimeout,
		WriteTimeout:      qaWriteTimeout,
		IdleTimeout:       qaIdleTimeout,
	}
	done := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), qaShutdownTimeout)
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

type previewService struct{}

func previewCredentials() *managementv1.WebAuthCredentials {
	return &managementv1.WebAuthCredentials{AccessToken: "preview-access", RefreshToken: "preview-refresh", CSRFToken: "preview-csrf",
		Assurance: managementv1.Normal, AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)}
}

func (previewService) Login(context.Context, managementv1.LocalLoginRequest) (managementv1.WebAuthLoginResult, error) {
	return managementv1.WebAuthLoginResult{Outcome: managementv1.Authenticated, Credentials: previewCredentials()}, nil
}
func (previewService) FinishLogin(context.Context, managementv1.WebAuthLoginMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	return *previewCredentials(), nil
}
func (previewService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "preview", Username: "preview", DisplayName: "Preview administrator", Role: managementv1.Administrator}, nil
}
func (previewService) Refresh(context.Context, managementclient.Auth) (managementv1.WebAuthCredentials, error) {
	return *previewCredentials(), nil
}
func (previewService) Logout(context.Context, managementclient.Auth) error { return nil }
func (previewService) ReplacePassword(context.Context, managementclient.Auth, managementv1.WebAuthPasswordReplaceRequest) (managementv1.WebAuthCredentials, error) {
	return *previewCredentials(), nil
}
func (previewService) MFAStatus(context.Context, managementclient.Auth) (managementv1.WebAuthMFAStatus, error) {
	return managementv1.WebAuthMFAStatus{}, nil
}
func (previewService) BeginMFA(context.Context, managementclient.Auth, bool, *string, *string) (managementv1.WebAuthMFAContinuation, error) {
	return managementv1.WebAuthMFAContinuation{}, managementclient.ErrUnavailable
}
func (previewService) FinishMFA(context.Context, managementclient.Auth, bool, managementv1.WebAuthMFAFinishRequest) (managementv1.WebAuthCredentials, error) {
	return managementv1.WebAuthCredentials{}, managementclient.ErrUnavailable
}
func (previewService) DisableMFA(context.Context, managementclient.Auth, *string) (managementv1.WebAuthCredentials, error) {
	return *previewCredentials(), nil
}
func (previewService) Sessions(context.Context, managementclient.Auth) (managementv1.WebAuthSessionPage, error) {
	return managementv1.WebAuthSessionPage{}, nil
}
func (previewService) RevokeSession(context.Context, managementclient.Auth, string, bool) error {
	return nil
}
func (previewService) SystemOverview(context.Context, managementclient.Auth) (managementv1.SystemOverview, error) {
	return managementv1.SystemOverview{}, nil
}
func (previewService) SystemAgent(context.Context, managementclient.Auth) (managementv1.SystemAgent, error) {
	return managementv1.SystemAgent{}, nil
}

var (
	_ browseredge.Service       = previewService{}
	_ browseredge.SystemService = previewService{}
)

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
