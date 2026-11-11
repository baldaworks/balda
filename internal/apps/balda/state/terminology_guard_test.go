package state

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var terminologyGuardPackages = map[string]map[string]bool{
	"internal/apps/balda/usercmd":         {},
	"internal/apps/balda/accessapp":       {},
	"internal/apps/balda/aliascmd":        {},
	"internal/apps/balda/chatarchivecmd":  {"Message": true},
	"internal/apps/balda/hookcmd":         {"Event": true},
	"internal/apps/balda/mcpcmd":          {},
	"internal/apps/balda/memorycmd":       {},
	"internal/apps/balda/plugincmd":       {},
	"internal/apps/balda/schedulecmd":     {},
	"internal/apps/balda/skillcmd":        {},
	"internal/apps/balda/webhookroutecmd": {},
	"internal/apps/balda/webhookcmd":      {"Admission": true},
}

func TestWebAuthTerminologyGuard(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve terminology guard path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	findings, err := scanWebAuthTerminology(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("ambiguous browser-session contracts:\n%s", strings.Join(findings, "\n"))
	}
}

func TestWebAuthTerminologyGuardRejectsAuthorityAndAllowsAIContracts(t *testing.T) {
	source := `package fixture
		type Authority struct { SessionID string; SessionVersion uint64 }
		type Message struct { SessionID string }`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	findings := ambiguousWebAuthIdentifiers("fixture", file, map[string]bool{"Message": true})
	if len(findings) != 2 || !strings.Contains(strings.Join(findings, " "), "Authority.SessionID") ||
		!strings.Contains(strings.Join(findings, " "), "Authority.SessionVersion") {
		t.Fatalf("findings = %v", findings)
	}
}

func scanWebAuthTerminology(root string) ([]string, error) {
	var findings []string
	for relative, aiTypes := range terminologyGuardPackages {
		directory := filepath.Join(root, filepath.FromSlash(relative))
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, 0)
			if err != nil {
				return nil, fmt.Errorf("parse %s/%s: %w", relative, entry.Name(), err)
			}
			findings = append(findings, ambiguousWebAuthIdentifiers(relative, file, aiTypes)...)
		}
	}
	slices.Sort(findings)
	return findings, nil
}

func ambiguousWebAuthIdentifiers(scope string, file *ast.File, aiTypes map[string]bool) []string {
	forbiddenTypes := map[string]bool{
		"SessionFamily": true, "SessionSummary": true, "SessionPage": true,
		"AccessSession": true, "RefreshSession": true, "RefreshRotation": true,
	}
	forbiddenFields := map[string]bool{"SessionID": true, "SessionVersion": true, "ActorSessionID": true}
	var findings []string
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, spec := range generic.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if forbiddenTypes[typeSpec.Name.Name] {
				findings = append(findings, scope+"."+typeSpec.Name.Name)
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok || aiTypes[typeSpec.Name.Name] {
				continue
			}
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					if forbiddenFields[name.Name] {
						findings = append(findings, scope+"."+typeSpec.Name.Name+"."+name.Name)
					}
				}
			}
		}
	}
	return findings
}
