package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNativeRegistersOnlyTrustedOwnedHooks(t *testing.T) {
	if os.Getenv("BALDA_NATIVE_HOOK_TEST") != "1" {
		t.Skip("enable BALDA_NATIVE_HOOK_TEST with a supported Codex CLI")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const userCommand = "printf '{}'"
	config := "features.plugins=false\nhooks.PreToolUse=[{hooks=[{type=\"command\",command=" + strconv.Quote(userCommand) + ",timeout=5}]}]\n"
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	baseline, err := probeCodex(t.Context(), nil, nil)
	if err != nil || len(baseline) != 1 || baseline[0].TimeoutSeconds != 5 {
		t.Fatalf("user hooks=%+v, %v", baseline, err)
	}
	config += "hooks.state={" + strconv.Quote(baseline[0].Key) + "={enabled=true,trusted_hash=" + strconv.Quote(baseline[0].Hash) + "}}\n"
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := prepareCodex(t.Context(), []string{"codex-acp", "--codex-args=--sandbox=workspace-write"})
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("trusted user hook lost: %+v", verified)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte(config)) {
		t.Fatal("native preparation changed user config")
	}
}
