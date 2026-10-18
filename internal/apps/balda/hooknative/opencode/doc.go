// Package opencode owns OpenCode native hook launch and wire adaptation.
package opencode

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativehost"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

const name = "opencode"

//go:embed opencode.mjs
var plugin []byte

type Adapter struct{ host hooknativehost.Host }

func New(host hooknativehost.Host) hooknative.Adapter { return &Adapter{host: host} }
func (*Adapter) Name() string                         { return name }
func (*Adapter) Supports(c agentconfig.Config) bool {
	return c.Type == agentconfig.AgentTypeOpenCodeACP
}
func (a *Adapter) Prepare(ctx context.Context, r hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	if r.SessionID == "" || strings.TrimSpace(r.CWD) == "" || r.LeafID == "" {
		return hooknative.PreparedLeaf{}, hookcmd.ErrInvalid
	}
	if !hookprovider.NativePlatformSupported() {
		return hooknative.PreparedLeaf{}, hookcmd.ErrUnsupported
	}
	resolved, err := agentconfig.NormalizeConfig(r.Config, a.host.Executable())
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	b, err := a.host.Bind(ctx, hookprovider.BindingScope{SessionID: r.SessionID, Provider: r.ProviderID, CWD: r.CWD})
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	if err := hooknativehost.WriteMetadata(b, name); err != nil {
		_ = b.Release()
		return hooknative.PreparedLeaf{}, err
	}
	return hooknative.PreparedLeaf{Config: hooknativehost.WrapLaunch(r.Config, a.host.Executable(), name, b, resolved), Release: b.Release}, nil
}

type Protocol struct{}

func (Protocol) Name() string { return name }
func (Protocol) Dispatch(context.Context, hookprovider.Binding, io.Reader, io.Writer) error {
	return hookcmd.ErrUnsupported
}
func (Protocol) Proxy(ctx context.Context, b hookprovider.Binding, path string, argv []string, in io.Reader, out, stderr io.Writer) error {
	return proxyruntime.Proxy(ctx, b, path, argv, in, out, stderr, behavior{})
}

type behavior struct{}

func (behavior) Prepare(_ context.Context, _ hookprovider.Binding, path string, argv []string, env map[string]string) ([]string, error) {
	config, err := object([]byte(env["OPENCODE_CONFIG_CONTENT"]))
	if err != nil {
		return nil, hookcmd.ErrUnsupported
	}
	var disabled bool
	_ = json.Unmarshal(config["disable_plugins"], &disabled)
	if disabled {
		return nil, hookcmd.ErrUnsupported
	}
	var plugins []json.RawMessage
	if len(config["plugin"]) != 0 && json.Unmarshal(config["plugin"], &plugins) != nil {
		return nil, hookcmd.ErrInvalid
	}
	pluginPath := filepath.Join(filepath.Dir(path), "opencode.mjs")
	if err := os.WriteFile(pluginPath, plugin, 0600); err != nil {
		return nil, err
	}
	urlPath := filepath.ToSlash(pluginPath)
	if filepath.VolumeName(pluginPath) != "" {
		urlPath = "/" + urlPath
	}
	pluginURL := (&url.URL{Scheme: "file", Path: urlPath}).String()
	config["plugin"] = encode(append(plugins, encode(pluginURL)))
	env["OPENCODE_CONFIG_CONTENT"] = string(encode(config))
	return argv, nil
}
func (behavior) RewriteRequest(line []byte) ([]byte, error) { return line, nil }
func (behavior) CheckResponse(line []byte) error            { return checkVersion(line) }
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil, hookcmd.ErrInvalid
	}
	return v, nil
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func checkVersion(line []byte) error {
	m, err := object(line)
	if err != nil {
		return hookcmd.ErrInvalid
	}
	result, err := object(m["result"])
	if err != nil || len(result["agentInfo"]) == 0 {
		return nil
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(result["agentInfo"], &info) != nil {
		return hookcmd.ErrUnsupported
	}
	minimum := []int{1, 19, 0}
	parts := strings.Split(strings.TrimPrefix(strings.Split(info.Version, "-")[0], "v"), ".")
	if len(parts) != 3 {
		return hookcmd.ErrUnsupported
	}
	for i, p := range parts {
		n, e := strconv.Atoi(p)
		if e != nil {
			return hookcmd.ErrUnsupported
		}
		if n > minimum[i] {
			return nil
		}
		if n < minimum[i] {
			return hookcmd.ErrUnsupported
		}
	}
	return nil
}
