package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMetadataInjectionPreservesProtocolAndUserSettings(t *testing.T) {
	for _, method := range []string{"session/new", "session/load", "session/resume"} {
		line := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":9007199254740993,"method":%q,"params":{"cwd":"/work","mcpServers":[{"name":"server"}],"_meta":{"systemPrompt":"keep instruction","claudeCode":{"options":{"settingSources":["user","project"],"settings":{"permissions":{"deny":["Bash(rm *)"]},"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[]}]}}}}}}}`, method))
		injected, err := inject(line)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(injected), "9007199254740993") || !strings.Contains(string(injected), "keep instruction") || !strings.Contains(string(injected), "Bash(rm *)") || !strings.Contains(string(injected), "settingSources") || !strings.Contains(string(injected), nativeCommand()[:2]) {
			t.Fatalf("metadata lost: %s", injected)
		}
		var message map[string]json.RawMessage
		_ = json.Unmarshal(injected, &message)
		params, _ := object(message["params"])
		meta, _ := object(params["_meta"])
		c, _ := object(meta["claudeCode"])
		options, _ := object(c["options"])
		extra, _ := object(options["extraArgs"])
		var inline string
		_ = json.Unmarshal(extra["settings"], &inline)
		settings, _ := object([]byte(inline))
		hooks, _ := object(settings["hooks"])
		var groups []json.RawMessage
		if json.Unmarshal(hooks["PreToolUse"], &groups) != nil || len(groups) != 2 {
			t.Fatal("user hooks overwritten")
		}
	}
	line := []byte(`{"jsonrpc":"2.0","method":"session/prompt","params":{"prompt":[{"type":"text","text":"$(literal)"}]}}`)
	got, err := inject(line)
	if err != nil || !bytes.Equal(got, line) {
		t.Fatal("unrelated ACP request changed")
	}
}
