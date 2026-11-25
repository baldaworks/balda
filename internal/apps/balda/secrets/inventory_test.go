package secrets

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const primaryPrimitiveOwner = "internal/apps/balda/secrets/protector.go"

var primitiveImports = map[string]map[string]bool{
	"internal/apps/balda/secrets/protector.go": {
		"crypto/aes":    true,
		"crypto/cipher": true,
		"crypto/rand":   true,
	},
	"internal/apps/balda/secrets/root.go": {
		"crypto/cipher": true,
		"crypto/hkdf":   true,
		"crypto/sha256": true,
	},
}

type inventoryEntry struct {
	scopeID          string
	scopeLabel       string
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
	sealOwners := discoverProtectorOperationOwners(t, root)
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
	sealOwners := discoverProtectorOperationOwners(t, root)
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
		{name: "unregistered scope label", entries: replaceScopeLabel(entries, 0, "balda/unregistered/v1"), exclusions: exclusions, imports: imports, sealOwners: sealOwners, rootWiring: rootConstructors},
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

func TestEncryptionInventoryIgnoresRepositoryBuildArtifacts(t *testing.T) {
	t.Parallel()

	for _, path := range []string{".git", ".tmp", ".tmp/go-build123", "pack/callee", "pack/callee/cache", "vendor"} {
		if !skipProductionDirectory(path) {
			t.Errorf("skipProductionDirectory(%q) = false, want true", path)
		}
	}
	for _, path := range []string{".", "cmd", "internal/apps/balda", "pkg"} {
		if skipProductionDirectory(path) {
			t.Errorf("skipProductionDirectory(%q) = true, want false", path)
		}
	}
}

func TestConfiguredEncryptionRootsArePinnedToApplicationSecurityKey(t *testing.T) {
	root := repositoryRoot(t)
	fields, envNames := discoverConfiguredEncryptionRoots(t, root)
	wantFields := []string{
		"internal/apps/balda/config.go:MCPManagementConfig.CredentialKey:credential_key",
		"internal/apps/balda/config.go:SecurityConfig.Key:key",
	}
	if !slices.Equal(fields, wantFields) {
		t.Fatalf("configured encryption-root fields changed; balda.security.key is the sole active root and credential_key is migration rejection only\ngot:  %v\nwant: %v", fields, wantFields)
	}
	if len(envNames) > 0 {
		t.Fatalf("unexpected hard-coded encryption environment names: %v", envNames)
	}
}

func TestEncryptionDocumentationPublishesSingleRootCompatibilityContract(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{
		"docs/architecture/application-encryption.md",
		"docs/reference/configuration.md",
		"docs/reference/database.md",
		"docs/reference/backoffice.md",
		"cmd/balda/balda.yaml",
	} {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if !strings.Contains(text, "balda.security.key") {
			t.Errorf("%s does not publish balda.security.key", relative)
		}
		if relative != "docs/reference/backoffice.md" && !strings.Contains(text, "BALDA_SECURITY_KEY") {
			t.Errorf("%s does not publish BALDA_SECURITY_KEY", relative)
		}
	}
	architecture, err := os.ReadFile(filepath.Join(root, "docs/architecture/application-encryption.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"HKDF-SHA-256", "version-1", "version 2", "database and exact application key", "rollback", "Adding a scope"} {
		if !strings.Contains(string(architecture), required) {
			t.Errorf("application encryption architecture is missing %q", required)
		}
	}

	err = filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(content), "\n")
		for index, line := range lines {
			if !strings.Contains(line, "balda.mcp_management.credential_key") && !strings.Contains(line, "BALDA_MCP_MANAGEMENT_CREDENTIAL_KEY") {
				continue
			}
			start, end := max(0, index-5), min(len(lines), index+6)
			context := strings.ToLower(strings.Join(lines[start:end], "\n"))
			if !strings.Contains(context, "obsolete") && !strings.Contains(context, "migration") {
				t.Errorf("%s:%d presents the obsolete MCP root outside migration guidance", path, index+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func validateInventory(root string, entries []inventoryEntry, exclusions []exclusion, imports []cryptoImport, sealOwners, rootConstructors []string) error {
	if len(entries) == 0 {
		return fmt.Errorf("encryption inventory is empty")
	}
	seenScopes := make(map[string]bool, len(entries))
	seenScopeLabels := make(map[Scope]bool, len(entries))
	seenConsumers := make(map[string]bool, len(entries))
	for _, entry := range entries {
		fields := []string{entry.scopeID, entry.scopeLabel, entry.consumer, entry.persistenceOwner, entry.aadOwner, entry.primitiveOwner, entry.rootSource, entry.rootWiring, entry.frame, entry.compatibility}
		for _, field := range fields {
			if strings.TrimSpace(field) == "" {
				return fmt.Errorf("scope %q has an empty field", entry.scopeID)
			}
		}
		if seenScopes[entry.scopeID] {
			return fmt.Errorf("scope %q has overlapping ownership", entry.scopeID)
		}
		seenScopes[entry.scopeID] = true
		label := Scope(entry.scopeLabel)
		if !registeredScope(label) {
			return fmt.Errorf("scope %q has unregistered label %q", entry.scopeID, entry.scopeLabel)
		}
		if seenScopeLabels[label] {
			return fmt.Errorf("scope label %q has overlapping ownership", label)
		}
		seenScopeLabels[label] = true
		if seenConsumers[entry.consumer] {
			return fmt.Errorf("consumer %q has overlapping ownership", entry.consumer)
		}
		seenConsumers[entry.consumer] = true
		for _, evidence := range []string{entry.consumer, entry.persistenceOwner, beforeColon(entry.aadOwner), beforeColon(entry.rootSource)} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(evidence))); err != nil {
				return fmt.Errorf("scope %q has stale evidence %q: %w", entry.scopeID, evidence, err)
			}
		}
		for _, evidence := range []string{entry.aadOwner, entry.rootSource} {
			if err := validateSymbolEvidence(root, evidence); err != nil {
				return fmt.Errorf("scope %q: %w", entry.scopeID, err)
			}
		}
		owners := strings.Split(entry.primitiveOwner, ";")
		if len(owners) != len(primitiveImports) {
			return fmt.Errorf("scope %q has incomplete primitive ownership %q", entry.scopeID, entry.primitiveOwner)
		}
		seenOwners := make(map[string]bool, len(owners))
		for _, owner := range owners {
			if seenOwners[owner] {
				return fmt.Errorf("scope %q has duplicate primitive owner %q", entry.scopeID, owner)
			}
			seenOwners[owner] = true
			if _, ok := primitiveImports[owner]; !ok {
				return fmt.Errorf("scope %q has unregistered primitive owner %q", entry.scopeID, owner)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(owner))); err != nil {
				return fmt.Errorf("scope %q has stale primitive owner %q: %w", entry.scopeID, owner, err)
			}
		}
	}
	if len(seenScopeLabels) != len(registeredScopes) {
		return fmt.Errorf("inventory covers %d registered scope labels, want %d", len(seenScopeLabels), len(registeredScopes))
	}
	registeredConsumers := make(map[string]bool, len(entries))
	registeredConstructors := make(map[string]bool)
	for _, entry := range entries {
		registeredConsumers[entry.consumer] = true
		for _, owner := range strings.Split(entry.rootWiring, ";") {
			registeredConstructors[owner] = true
		}
	}
	if err := compareOwnership("encryption consumer", registeredConsumers, sealOwners, primaryPrimitiveOwner); err != nil {
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
	seenPrimitiveImport := make(map[string]map[string]bool, len(primitiveImports))
	for _, item := range imports {
		if ownedImports := primitiveImports[item.file]; ownedImports[item.importPath] {
			if seenPrimitiveImport[item.file] == nil {
				seenPrimitiveImport[item.file] = make(map[string]bool)
			}
			seenPrimitiveImport[item.file][item.importPath] = true
			continue
		}
		if _, ok := excluded[item.importPath]; !ok {
			return fmt.Errorf("unowned cryptographic import %q in %s", item.importPath, item.file)
		}
		usedExclusions[item.importPath] = true
	}
	for owner, importPaths := range primitiveImports {
		for importPath := range importPaths {
			if !seenPrimitiveImport[owner][importPath] {
				return fmt.Errorf("registered primitive import %q in %s is stale", importPath, owner)
			}
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

func discoverProtectorOperationOwners(t *testing.T, root string) []string {
	t.Helper()
	const secretsImport = "github.com/baldaworks/balda/internal/apps/balda/secrets"
	packageProtectors := make(map[string]map[string]bool)
	err := walkProductionGo(root, func(_ string, rel string, file *ast.File) error {
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
		if len(aliases) == 0 {
			return nil
		}
		directory := filepath.ToSlash(filepath.Dir(rel))
		protectors := packageProtectors[directory]
		if protectors == nil {
			protectors = make(map[string]bool)
			packageProtectors[directory] = protectors
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.Field:
				if isProtectorType(declaration.Type, aliases) {
					for _, name := range declaration.Names {
						protectors[name.Name] = true
					}
				}
			case *ast.ValueSpec:
				if isProtectorType(declaration.Type, aliases) {
					for _, name := range declaration.Names {
						protectors[name.Name] = true
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var owners []string
	err = walkProductionGo(root, func(_ string, rel string, file *ast.File) error {
		protectors := packageProtectors[filepath.ToSlash(filepath.Dir(rel))]
		if len(protectors) == 0 {
			return nil
		}
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Seal" && method.Sel.Name != "Open" {
				return true
			}
			switch receiver := method.X.(type) {
			case *ast.Ident:
				found = protectors[receiver.Name]
			case *ast.SelectorExpr:
				found = protectors[receiver.Sel.Name]
			}
			return !found
		})
		if found {
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

func isProtectorType(expression ast.Expr, aliases map[string]bool) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Protector" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && aliases[identifier.Name]
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
			if identifierOK && (selector.Sel.Name == "New" || selector.Sel.Name == "NewRoot") && aliases[identifier.Name] {
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

func discoverConfiguredEncryptionRoots(t *testing.T, root string) ([]string, []string) {
	t.Helper()
	var fields, envNames []string
	err := walkProductionGo(root, func(_ string, rel string, file *ast.File) error {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					if field.Tag == nil || len(field.Names) != 1 {
						continue
					}
					rawTag, err := strconv.Unquote(field.Tag.Value)
					if err != nil {
						return err
					}
					mapTag := strings.Split(reflect.StructTag(rawTag).Get("mapstructure"), ",")[0]
					if (field.Names[0].Name == "Key" && rootKeyLike(strings.TrimSuffix(typeSpec.Name.Name, "Config")+"Key")) || rootKeyLike(field.Names[0].Name) || rootKeyLike(mapTag) {
						fields = append(fields, rel+":"+typeSpec.Name.Name+"."+field.Names[0].Name+":"+mapTag)
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && strings.HasPrefix(value, "BALDA_") && rootKeyLike(value) {
				envNames = append(envNames, value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(fields)
	sort.Strings(envNames)
	envNames = slices.Compact(envNames)
	return fields, envNames
}

func rootKeyLike(value string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(value))
	for _, marker := range []string{"securitykey", "credentialkey", "encryptionkey", "encryptkey", "masterkey", "rootkey", "secretkey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	if strings.Contains(normalized, "encrypt") || strings.Contains(normalized, "crypto") {
		for _, material := range []string{"key", "secret", "credential", "password", "passphrase", "root", "master"} {
			if strings.Contains(normalized, material) {
				return true
			}
		}
	}
	return false
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
			if skipProductionDirectory(rel) {
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

func skipProductionDirectory(rel string) bool {
	return rel == ".git" || rel == ".tmp" || strings.HasPrefix(rel, ".tmp/") ||
		rel == "pack/callee" || strings.HasPrefix(rel, "pack/callee/") || rel == "vendor"
}

func providerOwnedCryptoImport(importPath string) bool {
	lower := strings.ToLower(importPath)
	return strings.Contains(lower, "keyring") || strings.Contains(lower, "secretmanager") || strings.Contains(lower, "keymanagement") || strings.Contains(lower, "vault")
}

func readInventory(t *testing.T, path string) []inventoryEntry {
	t.Helper()
	rows := readTSV(t, path, 10)
	if got := strings.Join(rows[0], "\t"); got != "scope_id\tscope_label\tconsumer\tpersistence_owner\taad_owner\tprimitive_owner\troot_source\troot_wiring\tframe\tcompatibility" {
		t.Fatalf("unexpected inventory header: %s", got)
	}
	entries := make([]inventoryEntry, 0, len(rows)-1)
	for _, row := range rows[1:] {
		entries = append(entries, inventoryEntry{row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[7], row[8], row[9]})
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
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()
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

func replaceScopeLabel(entries []inventoryEntry, index int, label string) []inventoryEntry {
	cloned := append([]inventoryEntry(nil), entries...)
	cloned[index].scopeLabel = label
	return cloned
}
