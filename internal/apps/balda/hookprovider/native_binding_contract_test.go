package hookprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
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
	service := &Service{runner: runnerFunc(runner), bindings: make(map[string]*boundSession)}
	t.Cleanup(func() { _ = service.Close() })
	trusted := hookcmd.Event{SessionID: "trusted-session", Provider: "opaque-provider", CWD: t.TempDir(), Source: "native"}
	// The empty legacy kind deliberately avoids exercising the provider wire
	// dispatcher. TASK-003 removes this transitional bind parameter entirely.
	path, release, err := service.bind("", trusted)
	if err != nil {
		t.Fatal(err)
	}
	var binding Binding
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &binding) != nil {
		t.Fatalf("read current binding: %v", err)
	}
	return hooknativetest.BindingFixture{Binding: &currentBindingDriver{binding: binding, release: release}, Trusted: trusted}
}

type currentBindingDriver struct {
	binding Binding
	release func() error
}

func (d *currentBindingDriver) Invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return invoke(ctx, d.binding, event)
}

func (d *currentBindingDriver) InvokeUnauthenticated(ctx context.Context, event hookcmd.Event) error {
	untrusted := d.binding
	untrusted.Token = strings.Repeat("0", len(untrusted.Token))
	_, err := invoke(ctx, untrusted, event)
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
	return sendReady(ctx, d.binding, false)
}

func (d *currentBindingDriver) WaitReady(ctx context.Context) error {
	return waitReady(ctx, d.binding)
}

func (d *currentBindingDriver) Release() error { return d.release() }

type runnerFunc func(context.Context, hookcmd.Event) (hookcmd.Result, error)

func (f runnerFunc) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return f(ctx, event)
}
