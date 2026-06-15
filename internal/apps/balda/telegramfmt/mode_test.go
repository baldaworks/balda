package telegramfmt

import (
	"strings"
	"testing"
)

func TestNormalizeMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty defaults to rich_markdown", in: "", want: ModeRichMarkdown},
		{name: "whitespace defaults to rich_markdown", in: " \t\n ", want: ModeRichMarkdown},
		{name: "trim and lowercase rich html", in: "  RICH_HTML ", want: ModeRichHTML},
		{name: "trim and lowercase html", in: "  HTml ", want: ModeHTML},
		{name: "keeps unknown normalized", in: "  MD ", want: "md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeMode(tt.in); got != tt.want {
				t.Fatalf("NormalizeMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "default for empty", in: "", want: ModeRichMarkdown},
		{name: "rich_markdown", in: "rich_markdown", want: ModeRichMarkdown},
		{name: "rich_html", in: "rich_html", want: ModeRichHTML},
		{name: "markdownv2", in: "markdownv2", want: ModeMarkdownV2},
		{name: "trim and lowercase", in: "  HTml ", want: ModeHTML},
		{name: "none", in: "none", want: ModeNone},
		{name: "invalid", in: "md", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateMode(%q) error = nil, want non-nil", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateMode(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTelegramParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: ModeRichMarkdown, want: ""},
		{in: ModeRichHTML, want: ""},
		{in: ModeMarkdownV2, want: "MarkdownV2"},
		{in: ModeHTML, want: "HTML"},
		{in: ModeNone, want: ""},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		if got := TelegramParseMode(tt.in); got != tt.want {
			t.Fatalf("TelegramParseMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPromptRuleAndExample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          string
		wantRuleParts []string
		denyRuleParts []string
		wantExample   string
	}{
		{
			name: "rich_markdown",
			mode: ModeRichMarkdown,
			wantRuleParts: []string{
				"Use Telegram Rich Markdown",
				"Balda sends it through Telegram rich messages",
				RichMessagesDocsURL,
				"Do not pre-escape Telegram MarkdownV2 reserved characters",
			},
			denyRuleParts: []string{
				"Balda converts it to Telegram MarkdownV2",
				"Use Telegram HTML parse mode",
			},
			wantExample: richMarkdownExample,
		},
		{
			name: "rich_html",
			mode: ModeRichHTML,
			wantRuleParts: []string{
				"Use Telegram Rich HTML",
				RichMessagesDocsURL,
				"Balda escapes unsafe raw <, >, &",
			},
			denyRuleParts: []string{
				"Telegram HTML parse mode",
				"Balda converts it to Telegram MarkdownV2",
				"headings h1-h6",
				"details/summary",
				"lists, tables",
			},
			wantExample: "<h2>Build</h2><p><b>Status:</b> success</p><p>Run <code>balda start</code>.</p>",
		},
		{
			name: "markdownv2",
			mode: ModeMarkdownV2,
			wantRuleParts: []string{
				"Write normal Markdown or plain text",
				"Balda converts it to Telegram MarkdownV2",
				"do not pre-escape Telegram MarkdownV2 reserved characters",
			},
			denyRuleParts: []string{
				"Use Telegram HTML parse mode",
				"Escape raw <, >, & as entities",
			},
			wantExample: "**Build:** success. Run `balda start`.",
		},
		{
			name: "html",
			mode: ModeHTML,
			wantRuleParts: []string{
				"Use Telegram HTML parse mode",
				"Supported tags: b/strong, i/em, u/ins, s/strike/del",
				`pre with nested code class="language-..."`,
				"blockquote expandable",
				"tg-time unix/format",
				"Balda escapes unsafe raw <, >, &",
				"preserving supported Telegram HTML tags",
			},
			denyRuleParts: []string{
				"Balda converts it to Telegram MarkdownV2",
				"do not pre-escape Telegram MarkdownV2 reserved characters",
			},
			wantExample: "<b>Build:</b> success. Run <code>balda start</code>.",
		},
		{
			name:          "none",
			mode:          ModeNone,
			wantRuleParts: []string{"Use plain text only", "Do not use Markdown or HTML markup"},
			denyRuleParts: []string{"Telegram MarkdownV2", "Telegram HTML parse mode"},
			wantExample:   "Build: success. Run balda start.",
		},
		{
			name: "unknown defaults to rich_markdown",
			mode: "md",
			wantRuleParts: []string{
				"Use Telegram Rich Markdown",
				"Balda sends it through Telegram rich messages",
				RichMessagesDocsURL,
				"Do not pre-escape Telegram MarkdownV2 reserved characters",
			},
			denyRuleParts: []string{"Use Telegram HTML parse mode"},
			wantExample:   richMarkdownExample,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotRule, gotExample := PromptRuleAndExample(tt.mode)
			for _, want := range tt.wantRuleParts {
				if !strings.Contains(gotRule, want) {
					t.Fatalf("PromptRuleAndExample(%q) rule = %q, want to contain %q", tt.mode, gotRule, want)
				}
			}
			for _, denied := range tt.denyRuleParts {
				if strings.Contains(gotRule, denied) {
					t.Fatalf("PromptRuleAndExample(%q) rule = %q, should not contain %q", tt.mode, gotRule, denied)
				}
			}
			if gotExample != tt.wantExample {
				t.Fatalf("PromptRuleAndExample(%q) example = %q, want %q", tt.mode, gotExample, tt.wantExample)
			}
		})
	}
}

func TestRichMarkdownPromptExampleCoversOfficialRichConstructs(t *testing.T) {
	t.Parallel()

	_, got := PromptRuleAndExample(ModeRichMarkdown)
	for _, want := range []string{
		"# Release notes",
		"**Status:**",
		"_verified_",
		"~~obsolete path removed~~",
		"==highlighted==",
		"||internal note hidden||",
		"[the runbook](https://example.com/runbook)",
		"@owner",
		"```bash\n",
		"- [x] Update dependencies",
		"1. Deploy",
		"> Keep this summary short.",
		"| Area | Result |",
		"[^1]",
		"$p95 < 250ms$",
		"<details>",
		"<summary>More context</summary>",
		"![diagram](https://example.com/diagram.png)",
		"\n---\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rich markdown example missing %q in:\n%s", want, got)
		}
	}
}
