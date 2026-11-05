package handlersfx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIngressDoesNotOwnSessionActivationPresentationOrDurableSettlement(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, violation := range ingressOwnershipViolations(file) {
			t.Errorf("%s: %s", name, violation)
		}
	}
}

func TestIngressSettlementOwnershipScanHandlesDeliveryAliases(t *testing.T) {
	t.Parallel()

	const deliveryImport = `"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"`
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{name: "default", source: "package fixture\nimport " + deliveryImport + "\nvar _ = deliverycmd.SettlementOutbox", want: true},
		{name: "alias", source: "package fixture\nimport dc " + deliveryImport + "\nvar _ = dc.SettlementOutbox", want: true},
		{name: "dot", source: "package fixture\nimport . " + deliveryImport + "\nvar _ = SettlementOutbox", want: true},
		{name: "unrelated selector", source: "package fixture\ntype x struct{ SettlementOutbox string }", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), test.name+".go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := len(ingressOwnershipViolations(file)) != 0
			if got != test.want {
				t.Fatalf("violation = %v, want %v", got, test.want)
			}
		})
	}
}

func ingressOwnershipViolations(file *ast.File) []string {
	const (
		deliveryImport = "github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
		sessionFormat  = "github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	)
	var violations []string
	deliveryAliases := make(map[string]struct{})
	dotDelivery := false
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			violations = append(violations, "has an invalid import path")
			continue
		}
		if importPath == sessionFormat || strings.Contains(importPath, "/channel/") && strings.HasSuffix(importPath, "/presentation") {
			violations = append(violations, "imports session activation presentation "+importPath)
		}
		if importPath != deliveryImport {
			continue
		}
		alias := path.Base(importPath)
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		switch alias {
		case ".":
			dotDelivery = true
		case "_":
		default:
			deliveryAliases[alias] = struct{}{}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			owner, ok := value.X.(*ast.Ident)
			if !ok || value.Sel.Name != "SettlementOutbox" {
				return true
			}
			if _, ok := deliveryAliases[owner.Name]; ok {
				violations = append(violations, "owns durable delivery settlement")
			}
		case *ast.Ident:
			if dotDelivery && value.Name == "SettlementOutbox" {
				violations = append(violations, "owns durable delivery settlement")
			}
		}
		return true
	})
	return violations
}
