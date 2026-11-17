package backoffice

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var qaApplicationEvidence = map[string]string{
	"TestAliasesBrowserWorkflow":                                  "internal/apps/backoffice/aliases_browser_test.go",
	"TestBackofficeMCPBrowserWorkflow":                            "internal/apps/balda/backoffice_mcp_browser_test.go",
	"TestBackofficeMCPOAuthBrowserWorkflow":                       "internal/apps/balda/backoffice_mcp_oauth_browser_test.go",
	"TestBackofficePluginsBrowserWorkflow":                        "internal/apps/balda/backoffice_plugins_browser_test.go",
	"TestBackofficeSchedulesBrowserWorkflow":                      "internal/apps/balda/backoffice_schedules_browser_test.go",
	"TestBackofficeSkillsBrowserWorkflow":                         "internal/apps/balda/backoffice_skills_browser_test.go",
	"TestBackofficeWebhooksBrowserWorkflow":                       "internal/apps/balda/backoffice_webhooks_browser_test.go",
	"TestChatArchiveHTTPAuthority":                                "internal/apps/backoffice/chat_archive_test.go",
	"TestChatArchiveHTTPTranscript":                               "internal/apps/backoffice/chat_archive_test.go",
	"TestChatArchiveLayoutBrowser":                                "internal/apps/backoffice/chat_archive_layout_browser_test.go",
	"TestFactMemoryApplicationBrowser":                            "internal/apps/backoffice/fact_memory_browser_test.go",
	"TestFactMemoryLayoutBrowser":                                 "internal/apps/backoffice/fact_memory_layout_browser_test.go",
	"TestHooksApplicationBrowser":                                 "internal/apps/backoffice/hooks_browser_test.go",
	"TestHooksLayoutBrowser":                                      "internal/apps/backoffice/hooks_layout_browser_test.go",
	"TestHTTPAppAccessAdministrationNoJSHTMXAndRoleBoundary":      "internal/apps/backoffice/httpapp_test.go",
	"TestHTTPAppAccountRotationAndCurrentFamilyRevocation":        "internal/apps/backoffice/httpapp_test.go",
	"TestHTTPAppAuditFilteringPaginationRedactionAndRoleBoundary": "internal/apps/backoffice/httpapp_test.go",
	"TestHTTPAppBindingBrowserWorkflow":                           "internal/apps/backoffice/bindings_browser_test.go",
	"TestHTTPAppBrowserWorkflow":                                  "internal/apps/backoffice/browser_test.go",
	"TestHTTPAppWebAuthnBrowserWorkflow":                          "internal/apps/backoffice/webauthn_browser_test.go",
	"TestPluginsLayoutBrowser":                                    "internal/apps/backoffice/plugins_layout_browser_test.go",
	"TestProvidersBrowserManagementAndSecretBoundary":             "internal/apps/backoffice/providers_test.go",
	"TestProvidersLayoutBrowser":                                  "internal/apps/backoffice/providers_layout_browser_test.go",
	"TestSchedulesAliasesLayoutBrowser":                           "internal/apps/backoffice/schedules_aliases_layout_browser_test.go",
	"TestSkillsLayoutBrowser":                                     "internal/apps/backoffice/skills_layout_browser_test.go",
}

func TestQAStateMatrixEvidenceIsCurrent(t *testing.T) {
	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(filepath.Join(repositoryRoot, "docs/reference/backoffice-ui-review.md"))
	if err != nil {
		t.Fatal(err)
	}

	gallery := make(map[string]bool)
	for _, entry := range append(qaChatArchiveEntries(), qaEntries...) {
		if entry.gallery {
			gallery[entry.name] = true
		}
	}

	wantFamilies := []string{"Authentication", "Overview", "Agent / bindings", "Chat sessions", "Plugins", "Skills", "MCP", "Schedules", "Webhooks", "Aliases", "Agent hooks", "Fact memory", "Providers", "Access", "Audit", "Account"}
	var gotFamilies []string
	evidencePattern := regexp.MustCompile("`([GA]:[^`]+)`")
	inMatrix := false
	for _, line := range strings.Split(string(document), "\n") {
		if line == "## Maintained page-family state matrix" {
			inMatrix = true
			continue
		}
		if inMatrix && strings.HasPrefix(line, "## ") {
			break
		}
		if !inMatrix || !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| ---") || strings.HasPrefix(line, "| Page family") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 7 {
			t.Fatalf("matrix row has %d cells, want 7: %s", len(cells), line)
		}
		gotFamilies = append(gotFamilies, strings.TrimSpace(cells[0]))
		for column, cell := range cells[1:] {
			references := evidencePattern.FindAllStringSubmatch(cell, -1)
			if strings.Contains(cell, "Covered") && len(references) == 0 {
				t.Errorf("%s column %d is Covered without an exact G:/A: evidence ID", strings.TrimSpace(cells[0]), column+2)
			}
			for _, match := range references {
				reference := match[1]
				switch {
				case strings.HasPrefix(reference, "G:"):
					if !gallery[strings.TrimPrefix(reference, "G:")] {
						t.Errorf("%s references missing gallery fixture %s", strings.TrimSpace(cells[0]), reference)
					}
				case strings.HasPrefix(reference, "A:"):
					gate := strings.TrimPrefix(reference, "A:")
					source, ok := qaApplicationEvidence[gate]
					if !ok {
						t.Errorf("%s references unregistered application gate %s", strings.TrimSpace(cells[0]), reference)
						continue
					}
					contents, err := os.ReadFile(filepath.Join(repositoryRoot, source))
					if err != nil {
						t.Errorf("read application gate %s: %v", source, err)
						continue
					}
					if err := validateGoTestFunction(contents, gate); err != nil {
						t.Errorf("registered application gate %s in %s: %v", gate, source, err)
					}
				}
			}
		}
	}
	if !slices.Equal(gotFamilies, wantFamilies) {
		t.Errorf("matrix families = %q, want %q", gotFamilies, wantFamilies)
	}
}

func validateGoTestFunction(source []byte, name string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "evidence_test.go", source, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse source: %w", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name {
			continue
		}
		if function.Recv != nil {
			return fmt.Errorf("must be a top-level function without a receiver")
		}
		if function.Type.TypeParams != nil && len(function.Type.TypeParams.List) != 0 {
			return fmt.Errorf("must not declare type parameters")
		}
		if function.Type.Params == nil || len(function.Type.Params.List) != 1 {
			return fmt.Errorf("must accept exactly one *testing.T parameter")
		}
		parameter := function.Type.Params.List[0]
		if len(parameter.Names) > 1 {
			return fmt.Errorf("must accept exactly one *testing.T parameter")
		}
		pointer, ok := parameter.Type.(*ast.StarExpr)
		if !ok {
			return fmt.Errorf("parameter must have type *testing.T")
		}
		selector, ok := pointer.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "T" {
			return fmt.Errorf("parameter must have type *testing.T")
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "testing" {
			return fmt.Errorf("parameter must have type *testing.T")
		}
		if function.Type.Results != nil && len(function.Type.Results.List) != 0 {
			return fmt.Errorf("must not return a value")
		}
		return nil
	}
	return fmt.Errorf("top-level function %s not found", name)
}

func TestValidateGoTestFunction(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		wantErr bool
	}{
		{name: "valid named parameter", source: "package sample\nimport \"testing\"\nfunc TestGate(t *testing.T) {}"},
		{name: "valid unnamed parameter", source: "package sample\nimport \"testing\"\nfunc TestGate(*testing.T) {}"},
		{name: "comment decoy", source: "package sample\n// func TestGate(t *testing.T) {}", wantErr: true},
		{name: "string decoy", source: "package sample\nvar decoy = `func TestGate(t *testing.T) {}`", wantErr: true},
		{name: "method", source: "package sample\nimport \"testing\"\ntype suite struct{}\nfunc (suite) TestGate(t *testing.T) {}", wantErr: true},
		{name: "wrong parameter", source: "package sample\nfunc TestGate(value string) {}", wantErr: true},
		{name: "variadic", source: "package sample\nimport \"testing\"\nfunc TestGate(values ...*testing.T) {}", wantErr: true},
		{name: "type parameters", source: "package sample\nimport \"testing\"\nfunc TestGate[V any](t *testing.T) {}", wantErr: true},
		{name: "return value", source: "package sample\nimport \"testing\"\nfunc TestGate(t *testing.T) error { return nil }", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGoTestFunction([]byte(test.source), "TestGate")
			if (err != nil) != test.wantErr {
				t.Fatalf("validateGoTestFunction() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
