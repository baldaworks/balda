//go:build unix

package managementhttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/baldaworks/balda/internal/managementapi/v1/clienttest"
)

func TestUnixManagementPreservesGuardedReadAndMutation(t *testing.T) {
	dir, err := os.MkdirTemp("", "balda-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	aliases := &aliasFixture{}
	server := &http.Server{Handler: newManagementTestHandler(t, nil, nil, nil, aliases, nil, nil, nil), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client, err := managementv1.NewClientWithResponses("http://127.0.0.1",
		managementv1.WithHTTPClient(&http.Client{Transport: transport, Timeout: 5 * time.Second}),
		managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := clienttest.Exercise(t.Context(), client, clienttest.Input{
		Username: browserAuthTestUsername, Password: browserAuthTestPassword,
		BrowserBinding: strings.Repeat("b", 32), CSRFToken: strings.Repeat("c", 32),
		AliasName: "unix-client", LocatorRef: "telegram:123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Created.Name != "unix-client" || aliases.authority.UserID != browserAuthTestUsername || aliases.authority.WebAuthSessionID == "" {
		t.Fatalf("Unix mutation lost authenticated authority: %#v", observation)
	}
	denied, err := client.CreateAliasWithResponse(t.Context(), managementv1.AliasCreateRequest{Name: "unauthorized", LocatorRef: "telegram:123"})
	if err != nil || denied.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unguarded Unix mutation: %#v, %v", denied, err)
	}
}
