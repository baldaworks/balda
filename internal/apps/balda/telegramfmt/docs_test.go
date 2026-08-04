package telegramfmt

import (
	"os"
	"strings"
	"testing"
)

func TestTelegramFormattingDocsCoverFormatterContract(t *testing.T) {
	t.Parallel()

	doc := readRepoDoc(t, "docs/telegram-formatting.md")
	for _, want := range []string{
		"`rich_markdown` (default)",
		"`rich_html`",
		"`none`",
		"fails configuration validation",
		"at most one",
		"Ambiguous transport errors",
		"authentication errors",
		"rate limits",
		"deterministic plain",
		"no `parse_mode`",
		`<blockquote expandable>`,
		`<tg-time unix="..." format="...">`,
		`<pre><code class="language-...">...</code></pre>`,
		`<tg-time datetime="...">`,
		`<code class="language-...">`,
		`<div>`,
		`<script>`,
		`&lt;`, `&gt;`, `&amp;`, `&quot;`,
		"decimal numeric entities",
		"hex numeric entities",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("telegram formatting docs missing %q", want)
		}
	}
}

func TestUserDocsLinkTelegramFormattingGuide(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"README.md", "docs/balda.md"} {
		doc := readRepoDoc(t, path)
		if !strings.Contains(doc, "telegram-formatting.md") {
			t.Fatalf("%s does not link telegram-formatting.md", path)
		}
	}
}

func TestUserDocsDocumentRuntimeStateHelper(t *testing.T) {
	t.Parallel()

	doc := readRepoDoc(t, "docs/balda.md")
	if !strings.Contains(doc, "task runtime-state") {
		t.Fatal("docs/balda.md does not document task runtime-state")
	}
}

func TestReadmeDocumentsBaldaConfigShapeAndMCPServers(t *testing.T) {
	t.Parallel()

	doc := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"## Configuration",
		"runtime:",
		"providers:",
		"mcp_servers:",
		"balda.provider",
		"balda.telegram.token",
		"balda.zulip.*",
		"balda.slack.*",
		"balda.webhooks.*",
		"balda.scheduler.jobs",
		"balda.workspace.*",
		"balda.mcp_servers",
		"docs/balda.md",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("README.md missing %q", want)
		}
	}
}

func TestDocsBaldaDocumentsAdvancedConfigAndMCPServers(t *testing.T) {
	t.Parallel()

	doc := readRepoDoc(t, "docs/balda.md")
	for _, want := range []string{
		"type: <provider_type>",
		"type: codex_acp",
		"codex",
		"opencode",
		"copilot",
		"gemini",
		"claude",
		"`balda.telegram.webhook.enabled`",
		"`balda.telegram.plan_updates`",
		"`balda.working_dir`",
		"`balda.state_dir`",
		"`balda.global_instruction`",
		"### MCP Server Configuration",
		"type: stdio",
		"type: http",
		"`runtime.mcp_servers`",
		"`runtime.providers.<id>.mcp_servers`",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("docs/balda.md missing %q", want)
		}
	}
}

func readRepoDoc(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile("../../../../" + path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
