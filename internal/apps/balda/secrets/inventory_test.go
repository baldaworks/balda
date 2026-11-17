package secrets

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const primitiveOwner = "internal/apps/balda/secrets/protector.go"

var primitiveImports = map[string]bool{
	"crypto/aes":    true,
	"crypto/cipher": true,
	"crypto/rand":   true,
}

type inventoryEntry struct {
	scopeID          string
	consumer         string
	persistenceOwner string
	aadOwner         string
	primitiveOwner   string
	rootSource       string
	rootWiring       string
	frame            string
	compatibility    string
}

type exclusion struct {
	importPath string
	category   string
	reason     string
}

type cryptoImport struct {
	file       string
	importPath string
}

func TestEncryptionInventoryMatchesProductionSources(t *testing.T) {
	root := repositoryRoot(t)
	entries := readInventory(t, filepath.Join(root, "internal/apps/balda/secrets/testdata/encryption-inventory.tsv"))
	exclusions := readExclusions(t, filepath.Join(root, "internal/apps/balda/secrets/testdata/crypto-exclusions.tsv"))
	imports := discoverCryptoImports(t, root)
	sealOwners := discoverMethodOwners(t, root, "Seal")
	rootConstructors := discoverSecretsConstructors(t, root)
	if err := validateInventory(root, entries, exclusions, imports, sealOwners, rootConstructors); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptionInventoryRejectsMissingOverlappingAndStaleOwnership(t *testing.T) {
	root := repositoryRoot(t)
	entries := readInventory(t, filepath.Join(root, "internal/apps/balda/secrets/testdata/encryption-inventory.tsv"))
	exclusions := readExclusions(t, filepath.Join(root, "internal/apps/balda/secrets/testdata/crypto-exclusions.tsv"))
	imports := discoverCryptoImports(t, root)
	sealOwners := discoverMethodOwners(t, root, "Seal")
	rootConstructors := discoverSecretsConstructors(t, root)

	tests := []struct {
		name       string
		entries    []inventoryEntry
		exclusions []exclusion
		imports    []cryptoImport
		sealOwners []string
		rootWiring []string
	}{
		{name: "missing cryptographic import", entries: entries, exclusions: exclusions, imports: appendClone(imports, cryptoImport{file: "internal/new.go", importPath: "crypto/des"}), sealOwners: sealOwners, rootWiring: rootConstructors},
		{name: "unowned encryption consumer", entries: entries, exclusions: exclusions, imports: imports, sealOwners: appendClone(sealOwners, "internal/new.go"), rootWiring: rootConstructors},
		{name: "unowned root constructor", entries: entries, exclusions: exclusions, imports: imports, sealOwners: sealOwners, rootWiring: appendClone(rootConstructors, "internal/new.go")},
		{name: "overlapping scope", entries: appendClone(entries, entries[0]), exclusions: exclusions, imports: imports, sealOwners: sealOwners, rootWiring: rootConstructors},
		{name: "stale consumer", entries: replaceConsumer(entries, 0, "internal/removed.go"), exclusions: exclusions, imports: imports, sealOwners: sealOwners, rootWiring: rootConstructors},
		{name: "stale exclusion", entries: entries, exclusions: appendClone(exclusions, exclusion{importPath: "crypto/md5", category: "digest", reason: "test"}), imports: imports, sealOwners: sealOwners, rootWiring: rootConstructors},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateInventory(root, test.entries, test.exclusions, test.imports, test.sealOwners, test.rootWiring); err == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
}

func validateInventory(root string, entries []inventoryEntry, exclusions []exclusion, imports []cryptoImport, sealOwners, rootConstructors []string) error {
	if len(entries) == 0 {
		return fmt.Errorf("encryption inventory is empty")
	}
	seenScopes := make(map[string]bool, len(entries))
	seenConsumers := make(map[string]bool, len(entries))
	for _, entry := range entries {
		fields := []string{entry.scopeID, entry.consumer, entry.persistenceOwner, entry.aadOwner, entry.primitiveOwner, entry.rootSource, entry.rootWiring, entry.frame, entry.compatibility}
		for _, field := range fields {
			if strings.TrimSpace(field) == "" {
				return fmt.Errorf("scope %q has an empty field", entry.scopeID)
			}
		}
		if seenScopes[entry.scopeID] {
			return fmt.Errorf("scope %q has overlapping ownership", entry.scopeID)
		}
		seenScopes[entry.scopeID] = true
		if seenConsumers[entry.consumer] {
			return fmt.Errorf("consumer %q has overlapping ownership", entry.consumer)
		}
		seenConsumers[entry.consumer] = true
		for _, evidence := range []string{entry.consumer, entry.persistenceOwner, beforeColon(entry.aadOwner), entry.primitiveOwner, beforeColon(entry.rootSource)} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(evidence))); err != nil {
				return fmt.Errorf("scope %q has stale evidence %q: %w", entry.scopeID, evidence, err)
			}
		}
		for _, evidence := range []string{entry.aadOwner, entry.rootSource} {
			if err := validateSymbolEvidence(root, evidence); err != nil {
				return fmt.Errorf("scope %q: %w", entry.scopeID, err)
			}
		}
		if entry.primitiveOwner != primitiveOwner {
			return fmt.Errorf("scope %q has unregistered primitive owner %q", entry.scopeID, entry.primitiveOwner)
		}
	}
	registeredConsumers := make(map[string]bool, len(entries))
	registeredConstructors := make(map[string]bool)
	for _, entry := range entries {
		registeredConsumers[entry.consumer] = true
		for _, owner := range strings.Split(entry.rootWiring, ";") {
			registeredConstructors[owner] = true
		}
	}
	if err := compareOwnership("encryption consumer", registeredConsumers, sealOwners, primitiveOwner); err != nil {
		return err
	}
	if err := compareOwnership("root constructor", registeredConstructors, rootConstructors, ""); err != nil {
		return err
	}

	excluded := make(map[string]exclusion, len(exclusions))
	for _, rule := range exclusions {
		if rule.importPath == "" || rule.category == "" || rule.reason == "" {
			return fmt.Errorf("incomplete cryptography exclusion: %#v", rule)
		}
		if _, exists := excluded[rule.importPath]; exists {
			return fmt.Errorf("duplicate exclusion for %q", rule.importPath)
		}
		excluded[rule.importPath] = rule
	}
	usedExclusions := make(map[string]bool, len(exclusions))
	seenPrimitiveImport := make(map[string]bool, len(primitiveImports))
	for _, item := range imports {
		if item.file == primitiveOwner && primitiveImports[item.importPath] {
			seenPrimitiveImport[item.importPath] = true
			continue
		}
		if _, ok := excluded[item.importPath]; !ok {
			return fmt.Errorf("unowned cryptographic import %q in %s", item.importPath, item.file)
		}
		usedExclusions[item.importPath] = true
	}
	for importPath := range primitiveImports {
		if !seenPrimitiveImport[importPath] {
			return fmt.Errorf("registered primitive import %q is stale", importPath)
		}
	}
	for importPath := range excluded {
		if !usedExclusions[importPath] {
			return fmt.Errorf("cryptography exclusion %q is stale", importPath)
		}
	}
	return nil
}

func compareOwnership(kind string, registered map[string]bool, discovered []string, ignored string) error {
	seen := make(map[string]bool, len(discovered))
	for _, owner := range discovered {
		if owner == ignored {
			continue
		}
		if !registered[owner] {
			return fmt.Errorf("unowned %s in %s", kind, owner)
		}
		seen[owner] = true
	}
	for owner := range registered {
		if !seen[owner] {
			return fmt.Errorf("registered %s %s is stale", kind, owner)
		}
	}
	return nil
}

func validateSymbolEvidence(root, evidence string) error {
	path, symbol, found := strings.Cut(evidence, ":")
	if !found || symbol == "" {
		return fmt.Errorf("symbol evidence %q is incomplete", evidence)
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Errorf("stale symbol evidence %q: %w", evidence, err)
	}
	for _, part := range strings.Split(symbol, ".") {
		if !strings.Contains(string(content), part) {
			return fmt.Errorf("stale symbol evidence %q", evidence)
		}
	}
	return nil
}

func discoverMethodOwners(t *testing.T, root, method string) []string {
	t.Helper()
	return discoverSourceOwners(t, root, func(file *ast.File) bool {
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == method {
				found = true
			}
			return true
		})
		return found
	})
}

func discoverSecretsConstructors(t *testing.T, root string) []string {
	t.Helper()
	const secretsImport = "github.com/baldaworks/balda/internal/apps/balda/secrets"
	return discoverSourceOwners(t, root, func(file *ast.File) bool {
		aliases := make(map[string]bool)
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil || path != secretsImport {
				continue
			}
			name := "secrets"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			aliases[name] = true
		}
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, identifierOK := selector.X.(*ast.Ident)
			if identifierOK && selector.Sel.Name == "New" && aliases[identifier.Name] {
				found = true
			}
			return true
		})
		return found
	})
}

func discoverSourceOwners(t *testing.T, root string, matches func(*ast.File) bool) []string {
	t.Helper()
	var owners []string
	err := walkProductionGo(root, func(path, rel string, file *ast.File) error {
		if matches(file) {
			owners = append(owners, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(owners)
	return owners
}

func discoverCryptoImports(t *testing.T, root string) []cryptoImport {
	t.Helper()
	var found []cryptoImport
	err := walkProductionGo(root, func(_ string, rel string, file *ast.File) error {
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "crypto" || strings.HasPrefix(importPath, "crypto/") || strings.HasPrefix(importPath, "golang.org/x/crypto/") || providerOwnedCryptoImport(importPath) {
				found = append(found, cryptoImport{file: rel, importPath: importPath})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].file == found[j].file {
			return found[i].importPath < found[j].importPath
		}
		return found[i].file < found[j].file
	})
	return found
}

func walkProductionGo(root string, visit func(path, rel string, file *ast.File) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel == ".git" || rel == "pack/callee" || strings.HasPrefix(rel, "pack/callee/") || rel == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		return visit(path, rel, file)
	})
}

func providerOwnedCryptoImport(importPath string) bool {
	lower := strings.ToLower(importPath)
	return strings.Contains(lower, "keyring") || strings.Contains(lower, "secretmanager") || strings.Contains(lower, "keymanagement") || strings.Contains(lower, "vault")
}

func readInventory(t *testing.T, path string) []inventoryEntry {
	t.Helper()
	rows := readTSV(t, path, 9)
	if got := strings.Join(rows[0], "\t"); got != "scope_id\tconsumer\tpersistence_owner\taad_owner\tprimitive_owner\troot_source\troot_wiring\tframe\tcompatibility" {
		t.Fatalf("unexpected inventory header: %s", got)
	}
	entries := make([]inventoryEntry, 0, len(rows)-1)
	for _, row := range rows[1:] {
		entries = append(entries, inventoryEntry{row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[7], row[8]})
	}
	return entries
}

func readExclusions(t *testing.T, path string) []exclusion {
	t.Helper()
	rows := readTSV(t, path, 3)
	if got := strings.Join(rows[0], "\t"); got != "import\tcategory\treason" {
		t.Fatalf("unexpected exclusions header: %s", got)
	}
	rules := make([]exclusion, 0, len(rows)-1)
	for _, row := range rows[1:] {
		rules = append(rules, exclusion{row[0], row[1], row[2]})
	}
	return rules
}

func readTSV(t *testing.T, path string, width int) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var rows [][]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		row := strings.Split(scanner.Text(), "\t")
		if len(row) != width {
			t.Fatalf("%s: expected %d fields, got %d", path, width, len(row))
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("%s: missing data rows", path)
	}
	return rows
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func beforeColon(value string) string {
	path, _, _ := strings.Cut(value, ":")
	return path
}

func appendClone[T any](values []T, value T) []T {
	cloned := append([]T(nil), values...)
	return append(cloned, value)
}

func replaceConsumer(entries []inventoryEntry, index int, consumer string) []inventoryEntry {
	cloned := append([]inventoryEntry(nil), entries...)
	cloned[index].consumer = consumer
	return cloned
}
