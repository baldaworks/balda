package backoffice

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This opt-in gate uses production views with synthetic, read-only QA data.
func TestSkillsLayoutBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "/balda/backoffice"} {
		t.Run(base, func(t *testing.T) {
			handler, err := QAHandler(base)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/skills-layout.cjs", server.URL, base)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("synthetic Skills layout: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}
