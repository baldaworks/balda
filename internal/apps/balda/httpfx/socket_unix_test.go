//go:build unix

package httpfx

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func socketTestPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "balda-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "core.sock")
}

func socketTestClient(t *testing.T, path string) *http.Client {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func TestUnixServerLifecycleAndReconnect(t *testing.T) {
	path := socketTestPath(t)
	client := socketTestClient(t, path)
	for _, body := range []string{"first", "replacement"} {
		server := NewNamedUnixServer("management", path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("constructor touched socket: %v", err)
		}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Stop(context.Background()) })
		if server.Addr().Network() != "unix" {
			t.Fatalf("bound network = %s", server.Addr().Network())
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("socket permissions: %v, %v", info, err)
		}
		response, err := client.Get("http://127.0.0.1/")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(got) != body {
			t.Fatalf("body = %q, %v; want %q", got, err, body)
		}
		contender := NewNamedUnixServer("contender", path, http.NotFoundHandler())
		if err := contender.Start(); err == nil {
			_ = contender.Stop(context.Background())
			t.Fatal("competing listener acquired active endpoint")
		}
		if err := server.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := server.Stop(context.Background()); err != nil {
			t.Fatalf("repeated stop: %v", err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned socket retained after stop: %v", err)
		}
	}
}

func TestUnixServerRecoversOwnedStaleSocket(t *testing.T) {
	path := socketTestPath(t)
	previous, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	previous.SetUnlinkOnClose(false)
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	server := NewNamedUnixServer("management", path, http.NotFoundHandler())
	if err := server.Start(); err != nil {
		t.Fatalf("stale endpoint prevented restart: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
}

func TestUnixServerPreservesExistingPaths(t *testing.T) {
	for _, kind := range []string{"regular file", "symlink", "active socket", "lock symlink", "shared parent"} {
		t.Run(kind, func(t *testing.T) {
			path := socketTestPath(t)
			checked := path
			switch kind {
			case "regular file":
				if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "lock symlink":
				if kind == "lock symlink" {
					checked += ".lock"
				}
				if err := os.Symlink("other-file", checked); err != nil {
					t.Fatal(err)
				}
			case "active socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
			case "shared parent":
				checked = filepath.Dir(path)
				if err := os.Chmod(checked, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(checked)
			if err != nil {
				t.Fatal(err)
			}
			server := NewNamedUnixServer("management", path, http.NotFoundHandler())
			if err := server.Start(); err == nil {
				_ = server.Stop(context.Background())
				t.Fatal("unsafe endpoint was accepted")
			}
			after, err := os.Lstat(checked)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing path was changed: %v", err)
			}
		})
	}
}

func TestUnixServerCleanupPreservesReplacement(t *testing.T) {
	path := socketTestPath(t)
	server := NewNamedUnixServer("management", path, http.NotFoundHandler())
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	contender := NewNamedUnixServer("contender", path, http.NotFoundHandler())
	if err := contender.Start(); err == nil {
		_ = contender.Stop(context.Background())
		t.Fatal("owner lock was lost with the socket pathname")
	}
	replacement, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) {
		t.Fatalf("closing predecessor removed replacement: %v", err)
	}
}
