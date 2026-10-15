package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativetest"
)

func TestNativeBindingContract(t *testing.T) {
	hooknativetest.RunBindingContract(t, newCurrentBindingFixture)
}

func newCurrentBindingFixture(t testing.TB, runner hooknativetest.Runner) hooknativetest.BindingFixture {
	t.Helper()
	service, err := New(runnerFunc(runner))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	trusted := hookcmd.Event{SessionID: "trusted-session", Provider: "opaque-provider", CWD: t.TempDir(), Source: "native"}
	binding, err := service.Bind(t.Context(), BindingScope{SessionID: trusted.SessionID, Provider: trusted.Provider, CWD: trusted.CWD})
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadBinding(binding.Reference())
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	return hooknativetest.BindingFixture{Binding: &currentBindingDriver{binding: read, release: binding.Release}, Trusted: trusted}
}

type currentBindingDriver struct {
	binding Binding
	release func() error
}

func (d *currentBindingDriver) Invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return d.binding.Invoke(ctx, event)
}

func (d *currentBindingDriver) InvokeUnauthenticated(ctx context.Context, event hookcmd.Event) error {
	untrusted := d.binding
	untrusted.Token = strings.Repeat("0", len(untrusted.Token))
	_, err := untrusted.Invoke(ctx, event)
	return err
}

func (d *currentBindingDriver) InvokeWithOrigin(ctx context.Context, event hookcmd.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.binding.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+d.binding.Token)
	request.Header.Set("Origin", "https://untrusted.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		return errors.New("cross-origin native hook request accepted")
	}
	return hookcmd.ErrUnavailable
}

func (d *currentBindingDriver) SignalReady(ctx context.Context) error {
	return d.binding.SignalReady(ctx)
}

func (d *currentBindingDriver) WaitReady(ctx context.Context) error {
	return d.binding.WaitReady(ctx)
}

func (d *currentBindingDriver) Release() error { return d.release() }

type runnerFunc func(context.Context, hookcmd.Event) (hookcmd.Result, error)

func (f runnerFunc) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return f(ctx, event)
}
