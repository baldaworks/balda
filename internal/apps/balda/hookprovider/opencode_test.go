package hookprovider

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

func TestOpenCodePluginRejectsBeforeEffectAndRewritesArguments(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required for the published OpenCode V1 plugin contract")
	}
	for _, mode := range []string{hookcmd.DecisionDeny, modeRewrite, modePost} {
		t.Run(mode, func(t *testing.T) {
			definition := nativeDefinition(t, mode)
			if mode == modePost {
				definition.Event = hookcmd.PostToolUse
			}
			service := testService(t, definition)
			cwd := t.TempDir()
			path, release, err := service.bind(kindOpenCode, hookcmd.Event{SessionID: "session", Provider: "provider", CWD: cwd})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = release() })
			environment := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"plugin":["existing-plugin"],"model":"existing-model"}`}
			if err := openCodeEnvironment(path, environment); err != nil {
				t.Fatal(err)
			}
			var config struct {
				Plugin []string `json:"plugin"`
				Model  string   `json:"model"`
			}
			if json.Unmarshal([]byte(environment["OPENCODE_CONFIG_CONTENT"]), &config) != nil || len(config.Plugin) != 2 || config.Plugin[0] != "existing-plugin" || config.Model != "existing-model" {
				t.Fatal("existing OpenCode config changed")
			}
			command := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", `
import { writeFile, access } from "node:fs/promises";
const { BaldaHooks } = await import(process.argv[1]);
const hooks = await BaldaHooks();
const input = { tool: "bash", sessionID: "native-session", callID: "call-1" };
const before = { args: { command: "original" } };
let denied = false;
try {
  await hooks["tool.execute.before"](input, before);
  await writeFile("side-effect", before.args.command);
  const after = { title: "tool", output: "result", metadata: { exit: 0 } };
  await hooks["tool.execute.after"]({ ...input, args: before.args }, after);
  process.stdout.write(JSON.stringify({ args: before.args, output: after.output }));
} catch (error) {
  denied = true;
  process.stdout.write(JSON.stringify({ denied, reason: error.message }));
}
`, config.Plugin[1])
			command.Dir = cwd
			command.Env = append(os.Environ(), bindingEnv+"="+path)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("OpenCode plugin: %v: %s", err, output)
			}
			var result struct {
				Denied bool              `json:"denied"`
				Args   map[string]string `json:"args"`
				Output string            `json:"output"`
			}
			if json.Unmarshal(output, &result) != nil {
				t.Fatalf("plugin output: %s", output)
			}
			switch mode {
			case hookcmd.DecisionDeny:
				if !result.Denied {
					t.Fatal("denial allowed a tool effect")
				}
				if _, err := os.Stat(filepath.Join(cwd, "side-effect")); !os.IsNotExist(err) {
					t.Fatal("denied tool caused an effect")
				}
			case modeRewrite:
				data, err := os.ReadFile(filepath.Join(cwd, "side-effect"))
				if err != nil || string(data) != "approved command" {
					t.Fatalf("rewrite did not reach tool: %s, %v", data, err)
				}
			case modePost:
				if result.Output != "result\n\n<balda_hook_context>\nchecked tool result\n</balda_hook_context>" {
					t.Fatalf("post context: %s", output)
				}
			}
		})
	}
}

func TestCodexNativeRegistersOnlyTrustedOwnedHooks(t *testing.T) {
	if os.Getenv("BALDA_NATIVE_HOOK_TEST") != "1" {
		t.Skip("enable BALDA_NATIVE_HOOK_TEST with a supported Codex CLI installed")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const userCommand = "printf '{}'"
	config := "features.plugins=false\nhooks.PreToolUse=[{hooks=[{type=\"command\",command=" + strconv.Quote(userCommand) + ",timeout=5}]}]\n"
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	baseline, err := probeCodex(t.Context(), nil, nil)
	if err != nil || len(baseline) != 1 || baseline[0].TimeoutSeconds != 5 {
		t.Fatalf("user hook discovery = %+v, %v", baseline, err)
	}
	config += "hooks.state={" + strconv.Quote(baseline[0].Key) + "={enabled=true,trusted_hash=" + strconv.Quote(baseline[0].Hash) + "}}\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := prepareCodex(t.Context(), []string{"codex-acp", "--codex-args=--sandbox=workspace-write"})
	if err != nil {
		t.Fatal(err)
	}
	if len(command) < 8 || command[1] != "--codex-args=--sandbox=workspace-write" {
		t.Fatalf("native arguments not preserved: %v", command)
	}
	var arguments []string
	for _, arg := range command[1:] {
		arguments = append(arguments, strings.TrimPrefix(arg, "--codex-args="))
	}
	verified, err := probeCodex(t.Context(), arguments, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hook := range verified {
		if hook.Command == userCommand {
			found = hook.Enabled && hook.Trust == "trusted" && hook.Hash == baseline[0].Hash
		}
	}
	if !found {
		t.Fatalf("trusted user hook lost its definition or trust: %+v", verified)
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(data, []byte(config)) {
		t.Fatal("native preparation modified user configuration")
	}
}
