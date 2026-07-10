package balda

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const baldaImportPrefix = "github.com/normahq/balda/internal/apps/balda/"

func TestArchitectureDependencyMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dir      string
		requires []string
	}{
		{
			name: "actor command contracts are the product wire leaf",
			dir:  "actorcmd",
			requires: []string{
				"github.com/normahq/balda/pkg/actorlayer",
			},
		},
		{
			name: "runtime host consumes product wire contracts",
			dir:  "execution",
			requires: []string{
				baldaImportPrefix + "actorcmd",
			},
		},
		{
			name: "product actors consume wire contracts",
			dir:  "actors",
			requires: []string{
				baldaImportPrefix + "actorcmd",
			},
		},
		{
			name: "queued turn use case owns session restoration",
			dir:  "sessionturn",
			requires: []string{
				baldaImportPrefix + "actors",
				baldaImportPrefix + "session",
			},
		},
		{
			name: "ingress wires the queued turn use case",
			dir:  "handlers",
			requires: []string{
				baldaImportPrefix + "sessionturn",
			},
		},
		{
			name: "bundled MCP has dedicated lifecycle ownership",
			dir:  "internalmcp",
			requires: []string{
				baldaImportPrefix + "controlmcp",
				baldaImportPrefix + "session",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			imports := productionImports(t, test.dir)
			for _, required := range test.requires {
				if _, ok := imports[required]; !ok {
					t.Errorf("package %s direct imports = %v, want %q", test.dir, importNames(imports), required)
				}
			}
		})
	}
}

func TestActorCommandPackageRemainsAFrameworkLeaf(t *testing.T) {
	t.Parallel()

	imports := productionImports(t, "actorcmd")
	for path := range imports {
		if strings.HasPrefix(path, baldaImportPrefix) {
			t.Errorf("actorcmd imports application package %q; wire contracts must remain a leaf", path)
		}
	}
}

func productionImports(t *testing.T, relativeDir string) map[string]struct{} {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(relativeDir, "*.go"))
	if err != nil {
		t.Fatalf("Glob(%q) error = %v", relativeDir, err)
	}
	imports := make(map[string]struct{})
	files := token.NewFileSet()
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile(%q) error = %v", path, err)
		}
		for _, spec := range file.Decls {
			declaration, ok := spec.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, raw := range declaration.Specs {
				importSpec, ok := raw.(*ast.ImportSpec)
				if !ok {
					continue
				}
				name, err := strconv.Unquote(importSpec.Path.Value)
				if err != nil {
					t.Fatalf("Unquote(%q) error = %v", importSpec.Path.Value, err)
				}
				imports[name] = struct{}{}
			}
		}
	}
	return imports
}

func importNames(imports map[string]struct{}) []string {
	names := make([]string, 0, len(imports))
	for name := range imports {
		names = append(names, name)
	}
	return names
}
