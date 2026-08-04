package telegramfmt

import (
	"strings"
	"testing"
)

func TestValidateModeAcceptsCurrentModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ModeRichMarkdown},
		{input: " RICH_MARKDOWN ", want: ModeRichMarkdown},
		{input: " RICH_HTML ", want: ModeRichHTML},
		{input: " NONE ", want: ModeNone},
	}
	for _, test := range tests {
		got, err := ValidateMode(test.input)
		if err != nil {
			t.Fatalf("ValidateMode(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("ValidateMode(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestValidateModeRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	if _, err := ValidateMode("unsupported"); err == nil {
		t.Fatal("ValidateMode(unsupported) error = nil, want non-nil")
	}
}

func TestPromptRuleAndExampleCoversCurrentModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode      string
		wantRule  string
		wantValue string
	}{
		{mode: ModeRichMarkdown, wantRule: "Use Telegram Rich Markdown", wantValue: "# Release notes"},
		{mode: ModeRichHTML, wantRule: "Use Telegram Rich HTML", wantValue: "<h2>Build</h2>"},
		{mode: ModeNone, wantRule: "Use plain text only", wantValue: "Build: success"},
	}
	for _, test := range tests {
		rule, example := PromptRuleAndExample(test.mode)
		if !strings.Contains(rule, test.wantRule) || !strings.Contains(example, test.wantValue) {
			t.Fatalf("PromptRuleAndExample(%q) = (%q, %q)", test.mode, rule, example)
		}
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
		"```bash\n",
		"- [x] Update dependencies",
		"| Area | Result |",
		"<details>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rich markdown example missing %q in:\n%s", want, got)
		}
	}
}
