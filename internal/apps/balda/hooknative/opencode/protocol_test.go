package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

func TestPreparePreservesConfigAndInstallsPrivatePlugin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binding.json")
	environment := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"plugin":["existing-plugin"],"model":"existing-model"}`}
	argv := []string{"opencode-acp"}
	got, err := (behavior{}).Prepare(context.Background(), hookprovider.Binding{}, path, argv, environment)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != argv[0] {
		t.Fatalf("command changed: %q", got)
	}
	var config struct {
		Plugin []string `json:"plugin"`
		Model  string   `json:"model"`
	}
	if err := json.Unmarshal([]byte(environment["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Plugin) != 2 || config.Plugin[0] != "existing-plugin" || config.Model != "existing-model" {
		t.Fatalf("config changed: %+v", config)
	}
	info, err := os.Stat(filepath.Join(dir, "opencode.mjs"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("plugin permissions: %v, %v", info, err)
	}
}

type runnerFunc func(context.Context, hookcmd.Event) (hookcmd.Result, error)

func (f runnerFunc) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return f(ctx, event)
}

func TestPluginAppliesNativeDecisions(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required")
	}
	for _, mode := range []string{"deny", "rewrite", "post"} {
		t.Run(mode, func(t *testing.T) {
			service, err := hookprovider.New(runnerFunc(func(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
				switch mode {
				case "deny":
					if event.Name == hookcmd.PreToolUse {
						return hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: "blocked"}, nil
					}
				case "rewrite":
					if event.Name == hookcmd.PreToolUse {
						return hookcmd.Result{UpdatedInput: json.RawMessage(`{"command":"approved"}`)}, nil
					}
				case "post":
					if event.Name == hookcmd.PostToolUse {
						return hookcmd.Result{AdditionalContext: "checked"}, nil
					}
				}
				return hookcmd.Result{}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = service.Close() }()
			dir := t.TempDir()
			binding, err := service.Bind(t.Context(), hookprovider.BindingScope{SessionID: "session", Provider: "provider", CWD: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = binding.Release() }()
			env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{}`}
			if _, err := (behavior{}).Prepare(t.Context(), *binding, binding.Reference(), []string{"agent"}, env); err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Plugin []string `json:"plugin"`
			}
			_ = json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg)
			script := `const {BaldaHooks}=await import(process.argv[1]);const h=await BaldaHooks();const i={tool:"bash",sessionID:"s",callID:"c"};const b={args:{command:"original"}};try{await h["tool.execute.before"](i,b);const a={title:"t",output:"result",metadata:{}};await h["tool.execute.after"]({...i,args:b.args},a);process.stdout.write(JSON.stringify({args:b.args,output:a.output}))}catch(e){process.stdout.write(JSON.stringify({denied:true,reason:e.message}))}`
			command := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script, cfg.Plugin[0])
			command.Dir = dir
			command.Env = append(os.Environ(), proxyruntime.BindingEnv+"="+binding.Reference())
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("plugin: %v: %s", err, output)
			}
			if mode == "deny" && !bytes.Contains(output, []byte(`"denied":true`)) {
				t.Fatalf("deny output: %s", output)
			}
			if mode == "rewrite" && !bytes.Contains(output, []byte(`"command":"approved"`)) {
				t.Fatalf("rewrite output: %s", output)
			}
			if mode == "post" && !bytes.Contains(output, []byte("checked")) {
				t.Fatalf("post output: %s", output)
			}
		})
	}
}
