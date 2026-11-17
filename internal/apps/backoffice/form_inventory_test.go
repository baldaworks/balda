package backoffice

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const productionFormInventory = "testdata/form-inventory.tsv"

const productionFormRouteInventory = "testdata/form-route-inventory.tsv"

const productionFormEvidenceInventory = "testdata/form-evidence.tsv"

const productionFormContractInventory = "testdata/form-contract.tsv"

const productionFormFindingsInventory = "testdata/form-findings.tsv"

const productionFormExclusionsInventory = "testdata/form-exclusions.tsv"

const productionFormAuditDocumentation = "../../../docs/reference/backoffice-ui-review.md"

const closedFindingStatus = "closed"

type auditedForm struct {
	id       string
	template string
	ordinal  int
	method   string
	family   string
	owner    string
	actor    string
	mode     string
	routes   string
	variants string
}

type formEvidence struct {
	template      string
	authenticated string
}

type formFinding struct {
	id, severity, formIDs, repair, repairTask, verification, status string
}

type productionFormRoute struct {
	method, route, registrationOwner, handlerOwner string
	formIDs                                        []string
}

type formFieldMatrix struct {
	visible, hidden, readonly, required, optional, writeOnly, defaulted int
}

func closeFormAuditFile(t *testing.T, file *os.File) {
	t.Helper()
	if err := file.Close(); err != nil {
		t.Errorf("close form audit file: %v", err)
	}
}

func TestProductionFormInventoryBaseline(t *testing.T) {
	forms := readProductionFormInventory(t)
	if got, want := len(forms), 66; got != want {
		t.Fatalf("inventory forms = %d, want reviewed baseline %d", got, want)
	}
	ids := make(map[string]struct{}, len(forms))
	formsByID := make(map[string]auditedForm, len(forms))
	claimed := make(map[string]auditedForm, len(forms))
	for _, form := range forms {
		if _, exists := ids[form.id]; exists {
			t.Errorf("duplicate stable form id %q", form.id)
		}
		ids[form.id] = struct{}{}
		formsByID[form.id] = form
		claim := form.template + "#" + strconv.Itoa(form.ordinal)
		if _, exists := claimed[claim]; exists {
			t.Errorf("production declaration %q has overlapping ownership", claim)
		}
		claimed[claim] = form
		for field, value := range map[string]string{
			"family": form.family, "handler owner": form.owner, "actor": form.actor,
			"mode": form.mode, "routes": form.routes, "material variants": form.variants,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("form %q has no %s", form.id, field)
			}
		}
	}

	declarations := discoverProductionFormDeclarations(t)
	if len(declarations) != len(forms) {
		t.Fatalf("discovered production forms = %d, inventory = %d", len(declarations), len(forms))
	}
	for declaration, method := range declarations {
		form, ok := claimed[declaration]
		if !ok {
			t.Errorf("production declaration %q is not inventoried", declaration)
			continue
		}
		if form.method != method {
			t.Errorf("production declaration %q method = %s, inventory = %s", declaration, method, form.method)
		}
	}

	routeForms := readProductionFormRouteInventory(t)
	verifyProductionFormRoutes(t, forms, readProductionFormRoutes(t))
	evidence := readProductionFormEvidence(t)
	verifyProductionFormContracts(t, forms, evidence)
	findings := readProductionFormFindings(t)
	if problems := validateFormAuditContract(forms, routeForms, evidence, findings); len(problems) > 0 {
		for _, problem := range problems {
			t.Error(problem)
		}
	}
	verifyFindingReferences(t, findings)
	for formID, owners := range evidence {
		form := formsByID[formID]
		if owners.template != form.template+"#"+strconv.Itoa(form.ordinal) {
			t.Errorf("form %q template evidence = %q, want production declaration %s#%d", formID, owners.template, form.template, form.ordinal)
		}
		for _, reference := range strings.Split(owners.authenticated, ";") {
			verifyFormEvidenceReference(t, formID, reference)
		}
	}
}

func TestProductionFormAuditDocumentationIsCurrent(t *testing.T) {
	forms := readProductionFormInventory(t)
	body, err := os.ReadFile(productionFormAuditDocumentation)
	if err != nil {
		t.Fatal(err)
	}
	documentation := string(body)
	if !strings.Contains(documentation, "66 production forms in 17 families") {
		t.Fatal("form audit documentation lacks the reviewed inventory summary")
	}
	families := make(map[string]struct{})
	formsByID := make(map[string]auditedForm, len(forms))
	for _, form := range forms {
		families[form.family] = struct{}{}
		formsByID[form.id] = form
	}
	if len(families) != 17 {
		t.Fatalf("inventory families = %d, want documented baseline 17", len(families))
	}
	verifyDocumentedFormFamilies(t, documentation, formsByID)

	file, err := os.Open(productionFormFindingsInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)
	counts := map[string]int{}
	total := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.Split(line, "\t")
		if len(columns) != 8 {
			t.Fatalf("invalid finding row %q", line)
		}
		total++
		counts[columns[1]]++
		if columns[7] != closedFindingStatus {
			t.Errorf("finding %q is not closed", columns[0])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if total != 37 || counts["high"] != 13 || counts["medium"] != 24 || !strings.Contains(documentation, "37 findings: 13 high and 24 medium, all closed") {
		t.Fatalf("finding summary total=%d high=%d medium=%d is not reconciled with documentation", total, counts["high"], counts["medium"])
	}
}

func TestProductionFormAuditContractFailsClosed(t *testing.T) {
	base := []auditedForm{{id: "mutation", template: "example.tmpl", ordinal: 1, method: "POST", family: "example", owner: "mutate", actor: "administrator", mode: "htmx", routes: "/example", variants: "valid,invalid"}}
	routes := map[string]struct{}{"mutation": {}}
	evidence := map[string]formEvidence{"mutation": {template: "example_test.go#TestTemplate", authenticated: "example_test.go#TestMutation"}}
	tests := []struct {
		name     string
		forms    []auditedForm
		routes   map[string]struct{}
		evidence map[string]formEvidence
		findings []formFinding
	}{
		{name: "missing route mapping", forms: base, routes: map[string]struct{}{}, evidence: evidence},
		{name: "stale route mapping", forms: base, routes: map[string]struct{}{"mutation": {}, "removed": {}}, evidence: evidence},
		{name: "duplicate form ID", forms: append(append([]auditedForm{}, base...), base[0]), routes: routes, evidence: evidence},
		{name: "unowned state dimensions", forms: []auditedForm{{id: "mutation", method: "POST", family: "example"}}, routes: routes, evidence: evidence},
		{name: "missing authenticated evidence", forms: base, routes: routes, evidence: map[string]formEvidence{"mutation": {template: "example_test.go#TestTemplate"}}},
		{name: "unresolved finding", forms: base, routes: routes, evidence: evidence, findings: []formFinding{{id: "F-001", formIDs: "mutation", repair: "example.go#mutate", repairTask: "task-1", verification: "TestMutation", status: "open"}}},
		{name: "stale finding form ID", forms: base, routes: routes, evidence: evidence, findings: []formFinding{{id: "F-001", formIDs: "removed", repair: "example.go#mutate", repairTask: "task-1", verification: "TestMutation", status: closedFindingStatus}}},
		{name: "missing finding verification", forms: base, routes: routes, evidence: evidence, findings: []formFinding{{id: "F-001", formIDs: "mutation", repair: "example.go#mutate", repairTask: "task-1", status: closedFindingStatus}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if problems := validateFormAuditContract(test.forms, test.routes, test.evidence, test.findings); len(problems) == 0 {
				t.Fatal("invalid audit contract passed")
			}
		})
	}
	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if repositoryContainsReference(t, repositoryRoot, "missing-repair.go", "", false) || repositoryContainsReference(t, repositoryRoot, "", "TestMissingRegression", true) {
		t.Fatal("stale finding repair or verification reference passed")
	}
}

func TestProductionFormFieldMatrixIsClassified(t *testing.T) {
	forms := readProductionFormInventory(t)
	matrices := discoverProductionFormFields(t)
	for _, form := range forms {
		claim := form.template + "#" + strconv.Itoa(form.ordinal)
		matrix, ok := matrices[claim]
		if !ok {
			t.Errorf("form %q has no derived field matrix", form.id)
			continue
		}
		if matrix.visible+matrix.hidden == 0 {
			t.Errorf("form %q has no named visible or hidden fields", form.id)
		}
		if matrix.required+matrix.optional == 0 {
			t.Errorf("form %q has no required/optional field classification", form.id)
		}
		if matrix.readonly > matrix.visible || matrix.writeOnly > matrix.visible {
			t.Errorf("form %q has an invalid readonly/write-only classification: %+v", form.id, matrix)
		}
	}
}

func TestProductionFormControlsHaveProgrammaticLabels(t *testing.T) {
	paths, err := filepath.Glob("internal/webui/templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	controlPattern := regexp.MustCompile(`(?s)<(?:input|select|textarea)\b[^>]*>`)
	idPattern := regexp.MustCompile(`\bid="([^"]+)"`)
	exclusions := readProductionFormExclusions(t)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for formIndex, form := range formPattern.FindAll(body, -1) {
			if _, excluded := exclusions[filepath.Base(path)+"#"+strconv.Itoa(formIndex+1)]; excluded {
				continue
			}
			for _, control := range controlPattern.FindAll(form, -1) {
				tag := string(control)
				if strings.Contains(tag, `type="hidden"`) {
					continue
				}
				if strings.Contains(tag, `aria-label="`) || strings.Contains(tag, `aria-labelledby="`) {
					continue
				}
				ids := idPattern.FindSubmatch(control)
				if len(ids) != 2 {
					t.Errorf("%s form %d has a visible named control without an id: %s", filepath.Base(path), formIndex+1, tag)
					continue
				}
				id := string(ids[1])
				if !strings.Contains(string(form), `for="`+id+`"`) {
					t.Errorf("%s form %d control %q has no programmatic label", filepath.Base(path), formIndex+1, id)
				}
			}
		}
	}
}

func validateFormAuditContract(forms []auditedForm, routeForms map[string]struct{}, evidence map[string]formEvidence, findings []formFinding) []string {
	var problems []string
	ids := make(map[string]struct{}, len(forms))
	for _, form := range forms {
		if _, exists := ids[form.id]; exists {
			problems = append(problems, fmt.Sprintf("duplicate stable form id %q", form.id))
		}
		ids[form.id] = struct{}{}
		if strings.TrimSpace(form.variants) == "" {
			problems = append(problems, fmt.Sprintf("form %q has no owned state dimensions", form.id))
		}
		owner, ok := evidence[form.id]
		if !ok || strings.TrimSpace(owner.template) == "" {
			problems = append(problems, fmt.Sprintf("form %q has no production-template evidence", form.id))
		}
		if form.method == "POST" && (!ok || strings.TrimSpace(owner.authenticated) == "") {
			problems = append(problems, fmt.Sprintf("mutation form %q has no authenticated evidence", form.id))
		}
		if _, ok := routeForms[form.id]; !ok {
			problems = append(problems, fmt.Sprintf("form %q has no reviewed browser route owner", form.id))
		}
	}
	for id := range routeForms {
		if _, ok := ids[id]; !ok {
			problems = append(problems, fmt.Sprintf("reviewed browser route references stale form %q", id))
		}
	}
	for id := range evidence {
		if _, ok := ids[id]; !ok {
			problems = append(problems, fmt.Sprintf("evidence references stale form %q", id))
		}
	}
	for _, finding := range findings {
		for _, formID := range strings.Split(finding.formIDs, ",") {
			if _, ok := ids[formID]; !ok {
				problems = append(problems, fmt.Sprintf("finding %q references stale form %q", finding.id, formID))
			}
		}
		if finding.status != closedFindingStatus {
			problems = append(problems, fmt.Sprintf("finding %q is unresolved", finding.id))
		}
		if strings.TrimSpace(finding.repair) == "" || strings.TrimSpace(finding.repairTask) == "" || strings.TrimSpace(finding.verification) == "" {
			problems = append(problems, fmt.Sprintf("finding %q lacks repair and verification evidence", finding.id))
		}
	}
	return problems
}

func verifyFindingReferences(t *testing.T, findings []formFinding) {
	t.Helper()
	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		for _, reference := range strings.Split(finding.repair, ";") {
			parts := strings.Split(reference, "#")
			if len(parts) != 2 || !repositoryContainsReference(t, repositoryRoot, parts[0], "", false) {
				t.Errorf("finding %q has stale repair reference %q", finding.id, reference)
			}
		}
		for _, testName := range strings.Split(finding.verification, " and ") {
			if !repositoryContainsReference(t, repositoryRoot, "", testName, true) {
				t.Errorf("finding %q has stale verification test %q", finding.id, testName)
			}
		}
	}
}

func repositoryContainsReference(t *testing.T, root, pathHint, symbol string, testsOnly bool) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if found || (testsOnly && !strings.HasSuffix(path, "_test.go")) {
			return nil
		}
		if pathHint != "" {
			if strings.Contains(pathHint, "/") {
				if filepath.Clean(path) != filepath.Join(root, filepath.FromSlash(pathHint)) {
					return nil
				}
			} else if filepath.Base(path) != pathHint {
				return nil
			}
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		needle := symbol
		if testsOnly {
			needle = "func " + symbol + "("
		}
		found = strings.Contains(string(body), needle)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func verifyDocumentedFormFamilies(t *testing.T, documentation string, forms map[string]auditedForm) {
	t.Helper()
	displayFamilies := map[string]string{
		"Access": "access", "Account": "account", "Aliases": "aliases", "Audit": "audit", "Authentication": "authentication", "Bindings": "bindings", "Chat archive": "chat-archive", "Fact memory": "fact-memory", "Hooks": "hooks", "MCP": "mcp", "Plugin marketplaces": "plugin-marketplaces", "Plugins": "plugins", "Providers": "providers", "Schedules": "schedules", "Sessions": "sessions", "Skills": "skills", "Webhooks": "webhooks",
	}
	idPattern := regexp.MustCompile("`([^`]+)`")
	seen := make(map[string]struct{}, len(forms))
	inSection := false
	for _, line := range strings.Split(documentation, "\n") {
		if line == "## Authoritative production-form audit" {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if !inSection || !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| ---") || strings.HasPrefix(line, "| Family") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 2 {
			t.Fatalf("invalid documented form-family row %q", line)
		}
		family, ok := displayFamilies[strings.TrimSpace(cells[0])]
		if !ok {
			t.Errorf("unknown documented form family %q", strings.TrimSpace(cells[0]))
			continue
		}
		for _, match := range idPattern.FindAllStringSubmatch(cells[1], -1) {
			form, exists := forms[match[1]]
			if !exists {
				t.Errorf("documentation references stale form ID %q", match[1])
				continue
			}
			if form.family != family {
				t.Errorf("documented form %q is under family %q, want %q", form.id, family, form.family)
			}
			if _, duplicate := seen[form.id]; duplicate {
				t.Errorf("documented form ID %q appears more than once", form.id)
			}
			seen[form.id] = struct{}{}
		}
	}
	for id := range forms {
		if _, ok := seen[id]; !ok {
			t.Errorf("form audit documentation omits stable form ID %q", id)
		}
	}
}

func verifyFormEvidenceReference(t *testing.T, formID, reference string) {
	t.Helper()
	parts := strings.Split(reference, "#")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Errorf("form %q has invalid evidence reference %q", formID, reference)
		return
	}
	body, err := os.ReadFile(parts[0])
	if err != nil {
		t.Errorf("form %q evidence %q: %v", formID, reference, err)
		return
	}
	if !strings.Contains(string(body), "func "+parts[1]+"(") {
		t.Errorf("form %q evidence test %q does not resolve", formID, reference)
	}
}

func readProductionFormRouteInventory(t *testing.T) map[string]struct{} {
	t.Helper()
	file, err := os.Open(productionFormRouteInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)

	forms := make(map[string]struct{})
	routes := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 5 {
			t.Fatalf("%s:%d columns = %d, want 5", productionFormRouteInventory, line, len(columns))
		}
		route := columns[0] + " " + columns[1]
		if _, exists := routes[route]; exists {
			t.Errorf("duplicate reviewed route %q", route)
		}
		routes[route] = struct{}{}
		if strings.TrimSpace(columns[2]) == "" || strings.TrimSpace(columns[3]) == "" {
			t.Errorf("route %q lacks registration or handler owner", route)
		}
		for _, id := range strings.Split(columns[4], ",") {
			forms[id] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return forms
}

func readProductionFormRoutes(t *testing.T) []productionFormRoute {
	t.Helper()
	file, err := os.Open(productionFormRouteInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)
	var rows []productionFormRoute
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 5 {
			t.Fatalf("%s:%d columns = %d, want 5", productionFormRouteInventory, line, len(columns))
		}
		rows = append(rows, productionFormRoute{method: columns[0], route: columns[1], registrationOwner: columns[2], handlerOwner: columns[3], formIDs: strings.Split(columns[4], ",")})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func verifyProductionFormRoutes(t *testing.T, forms []auditedForm, rows []productionFormRoute) {
	t.Helper()
	byID := make(map[string]auditedForm, len(forms))
	claims := make(map[string][]productionFormRoute, len(forms))
	for _, form := range forms {
		byID[form.id] = form
	}
	for _, row := range rows {
		source, err := os.ReadFile(row.registrationOwner)
		if err != nil {
			t.Errorf("route %s %s registration owner: %v", row.method, row.route, err)
			continue
		}
		handlerName := row.handlerOwner[strings.LastIndex(row.handlerOwner, ".")+1:]
		if !strings.Contains(string(source), handlerName) {
			t.Errorf("route %s %s handler %q is absent from %s", row.method, row.route, row.handlerOwner, row.registrationOwner)
		}
		for _, id := range row.formIDs {
			claims[id] = append(claims[id], row)
		}
	}
	for id, form := range byID {
		owners := make(map[string]struct{})
		for _, owner := range strings.Split(form.owner, "|") {
			owners[owner] = struct{}{}
		}
		for _, route := range strings.Split(form.routes, ";") {
			var matched *productionFormRoute
			for index := range claims[id] {
				if claims[id][index].method == form.method && productionRouteMatches(claims[id][index].route, route) {
					matched = &claims[id][index]
					break
				}
			}
			if matched == nil {
				t.Errorf("form %q route %s %s is not mapped to a production registration", id, form.method, route)
				continue
			}
			if _, ok := owners[matched.handlerOwner]; !ok {
				t.Errorf("form %q route %s handler = %q, want one of %q", id, route, matched.handlerOwner, form.owner)
			}
		}
		for _, row := range claims[id] {
			matched := false
			for _, route := range strings.Split(form.routes, ";") {
				matched = matched || productionRouteMatches(row.route, route)
			}
			if !matched {
				t.Errorf("route inventory maps unclaimed route %q to form %q", row.method+" "+row.route, id)
			}
		}
	}

	provider, config := newHTTPAppTestState(t)
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	for _, row := range rows {
		if strings.Contains(row.route, "{configured_channel}") {
			continue
		}
		path := strings.ReplaceAll(row.route, "{configured_channel}", "telegram")
		path = parameter.ReplaceAllString(path, "test")
		request := httptest.NewRequest(row.method, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusNotFound || response.Code == http.StatusMethodNotAllowed {
			t.Errorf("production router does not register %s %s (status %d)", row.method, row.route, response.Code)
		}
	}
}

func productionRouteMatches(pattern, route string) bool {
	route = strings.SplitN(route, "?", 2)[0]
	segments := strings.Split(pattern, "/")
	for index, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[index] = `[^/?]+`
		} else {
			segments[index] = regexp.QuoteMeta(segment)
		}
	}
	return regexp.MustCompile("^" + strings.Join(segments, "/") + "$").MatchString(route)
}

func readProductionFormEvidence(t *testing.T) map[string]formEvidence {
	t.Helper()
	file, err := os.Open(productionFormEvidenceInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)

	evidence := make(map[string]formEvidence)
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 3 {
			t.Fatalf("%s:%d columns = %d, want 3", productionFormEvidenceInventory, line, len(columns))
		}
		if _, exists := evidence[columns[0]]; exists {
			t.Fatalf("%s:%d duplicate form %q", productionFormEvidenceInventory, line, columns[0])
		}
		evidence[columns[0]] = formEvidence{template: columns[1], authenticated: columns[2]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func readProductionFormFindings(t *testing.T) []formFinding {
	t.Helper()
	file, err := os.Open(productionFormFindingsInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)

	var findings []formFinding
	ids := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 8 {
			t.Fatalf("%s:%d columns = %d, want 8", productionFormFindingsInventory, line, len(columns))
		}
		if _, exists := ids[columns[0]]; exists {
			t.Fatalf("%s:%d duplicate finding %q", productionFormFindingsInventory, line, columns[0])
		}
		ids[columns[0]] = struct{}{}
		for column, value := range columns {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s:%d empty finding column %d", productionFormFindingsInventory, line, column+1)
			}
		}
		findings = append(findings, formFinding{id: columns[0], severity: columns[1], formIDs: columns[2], repair: columns[3], repairTask: columns[5], verification: columns[6], status: columns[7]})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return findings
}

func readProductionFormInventory(t *testing.T) []auditedForm {
	t.Helper()
	file, err := os.Open(productionFormInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)

	var forms []auditedForm
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 10 {
			t.Fatalf("%s:%d columns = %d, want 10", productionFormInventory, line, len(columns))
		}
		ordinal, err := strconv.Atoi(columns[2])
		if err != nil || ordinal < 1 {
			t.Fatalf("%s:%d invalid ordinal %q", productionFormInventory, line, columns[2])
		}
		forms = append(forms, auditedForm{
			id: columns[0], template: columns[1], ordinal: ordinal, method: columns[3],
			family: columns[4], owner: columns[5], actor: columns[6], mode: columns[7],
			routes: columns[8], variants: columns[9],
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return forms
}

func discoverProductionFormDeclarations(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("internal/webui/templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*>`)
	methodPattern := regexp.MustCompile(`\bmethod="([^"]+)"`)
	declarations := make(map[string]string)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for index, tag := range formPattern.FindAll(body, -1) {
			method := "GET"
			if match := methodPattern.FindSubmatch(tag); len(match) == 2 {
				method = strings.ToUpper(string(match[1]))
			}
			declarations[filepath.Base(path)+"#"+strconv.Itoa(index+1)] = method
		}
	}
	for claim, reason := range readProductionFormExclusions(t) {
		if _, exists := declarations[claim]; !exists {
			t.Errorf("form exclusion %q is stale", claim)
			continue
		}
		if len(strings.TrimSpace(reason)) < 20 {
			t.Errorf("form exclusion %q lacks a substantive reason", claim)
		}
		delete(declarations, claim)
	}
	return declarations
}

func discoverProductionFormFields(t *testing.T) map[string]formFieldMatrix {
	t.Helper()
	paths, err := filepath.Glob("internal/webui/templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	controlPattern := regexp.MustCompile(`(?s)<(?:input|select|textarea|button)\b[^>]*>`)
	namePattern := regexp.MustCompile(`\bname="[^"]+"`)
	matrices := make(map[string]formFieldMatrix)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for index, form := range formPattern.FindAll(body, -1) {
			var matrix formFieldMatrix
			for _, control := range controlPattern.FindAll(form, -1) {
				if !namePattern.Match(control) {
					continue
				}
				tag := string(control)
				if strings.Contains(tag, `type="hidden"`) {
					matrix.hidden++
				} else {
					matrix.visible++
					if strings.Contains(tag, ` readonly`) {
						matrix.readonly++
					}
					if strings.Contains(tag, ` data-write-only`) {
						matrix.writeOnly++
					}
				}
				if strings.Contains(tag, ` required`) {
					matrix.required++
				} else {
					matrix.optional++
				}
				if strings.Contains(tag, ` value="`) || strings.Contains(tag, ` checked`) || strings.Contains(tag, ` selected`) {
					matrix.defaulted++
				}
			}
			matrices[filepath.Base(path)+"#"+strconv.Itoa(index+1)] = matrix
		}
	}
	return matrices
}

func discoverProductionFormFieldSignatures(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("internal/webui/templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	controlPattern := regexp.MustCompile(`(?s)<(?:input|select|textarea|button)\b[^>]*>`)
	namePattern := regexp.MustCompile(`\bname="([^"]+)"`)
	requiredPattern := regexp.MustCompile(`\srequired(?:\s|>)`)
	readonlyPattern := regexp.MustCompile(`\sreadonly(?:\s|>)`)
	signatures := make(map[string]string)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for index, form := range formPattern.FindAll(body, -1) {
			var fields []string
			for _, control := range controlPattern.FindAll(form, -1) {
				match := namePattern.FindSubmatch(control)
				if len(match) != 2 {
					continue
				}
				tag := string(control)
				flags := []string{"visible", "optional", "editable", "retained", "blank"}
				if strings.Contains(tag, `type="hidden"`) {
					flags[0] = "hidden"
				}
				if requiredPattern.MatchString(tag) {
					flags[1] = "required"
				}
				if readonlyPattern.MatchString(tag) {
					flags[2] = "readonly"
				}
				if strings.Contains(tag, ` data-write-only`) {
					flags[3] = "write-only"
				}
				if strings.Contains(tag, ` value="`) || strings.Contains(tag, ` checked`) || strings.Contains(tag, ` selected`) {
					flags[4] = "defaulted"
				}
				fields = append(fields, string(match[1])+":"+strings.Join(flags, ":"))
			}
			sort.Strings(fields)
			signatures[filepath.Base(path)+"#"+strconv.Itoa(index+1)] = strings.Join(fields, ",")
		}
	}
	return signatures
}

func verifyProductionFormContracts(t *testing.T, forms []auditedForm, evidence map[string]formEvidence) {
	t.Helper()
	file, err := os.Open(productionFormContractInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)
	contracts := make(map[string][]string)
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 8 {
			t.Fatalf("%s:%d columns = %d, want 8", productionFormContractInventory, line, len(columns))
		}
		if _, duplicate := contracts[columns[0]]; duplicate {
			t.Fatalf("duplicate form contract %q", columns[0])
		}
		contracts[columns[0]] = columns
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	signatures := discoverProductionFormFieldSignatures(t)
	markup := discoverProductionFormMarkup(t)
	routes := readProductionFormRoutes(t)
	for _, form := range forms {
		contract, ok := contracts[form.id]
		if !ok {
			t.Errorf("form %q has no variant/field contract", form.id)
			continue
		}
		if contract[1] != form.variants {
			t.Errorf("form %q variants drifted: inventory=%q contract=%q", form.id, form.variants, contract[1])
		}
		if !strings.Contains(form.variants, ",") && !strings.Contains(form.variants, ";") {
			t.Errorf("form %q has no material state alternatives", form.id)
		}
		digest := sha256.Sum256([]byte(signatures[form.template+"#"+strconv.Itoa(form.ordinal)]))
		if contract[2] != hex.EncodeToString(digest[:]) {
			t.Errorf("form %q field ownership/classification drifted", form.id)
		}
		if owner, ok := evidence[form.id]; !ok || contract[3] != owner.authenticated {
			t.Errorf("form %q variant evidence drifted", form.id)
		}
		evidenceDigest := sha256.New()
		for _, reference := range strings.Split(contract[3], ";") {
			parts := strings.SplitN(reference, "#", 2)
			if len(parts) != 2 {
				t.Errorf("form %q has invalid variant evidence %q", form.id, reference)
				continue
			}
			body, err := os.ReadFile(parts[0])
			if err != nil {
				t.Errorf("form %q variant evidence %q: %v", form.id, reference, err)
				continue
			}
			evidenceDigest.Write([]byte(reference))
			evidenceDigest.Write([]byte{0})
			evidenceDigest.Write(body)
			evidenceDigest.Write([]byte{0})
		}
		if contract[4] != hex.EncodeToString(evidenceDigest.Sum(nil)) {
			t.Errorf("form %q authenticated variant evidence source drifted", form.id)
		}
		if digest := sha256.Sum256([]byte(markup[form.template+"#"+strconv.Itoa(form.ordinal)])); contract[5] != hex.EncodeToString(digest[:]) {
			t.Errorf("form %q conditional field applicability drifted", form.id)
		}
		stateDigest := sha256.New()
		for dimension, values := range strings.Split(form.variants, ";") {
			for _, state := range strings.Split(values, ",") {
				stateDigest.Write([]byte(strconv.Itoa(dimension) + ":" + state))
				stateDigest.Write([]byte{0})
				stateDigest.Write([]byte(contract[3]))
				stateDigest.Write([]byte{0})
				stateDigest.Write([]byte(contract[4]))
				stateDigest.Write([]byte{0})
			}
		}
		if contract[6] != hex.EncodeToString(stateDigest.Sum(nil)) {
			t.Errorf("form %q per-state evidence mapping drifted", form.id)
		}
		var formRoutes []productionFormRoute
		for _, row := range routes {
			for _, id := range row.formIDs {
				if id == form.id {
					formRoutes = append(formRoutes, row)
				}
			}
		}
		sort.Slice(formRoutes, func(i, j int) bool {
			return serializeProductionRoute(formRoutes[i]) < serializeProductionRoute(formRoutes[j])
		})
		routeDigest := sha256.New()
		for _, row := range formRoutes {
			routeDigest.Write([]byte(serializeProductionRoute(row)))
			routeDigest.Write([]byte{0})
			body, err := os.ReadFile(row.registrationOwner)
			if err != nil {
				t.Errorf("form %q route source: %v", form.id, err)
				continue
			}
			routeDigest.Write(body)
			routeDigest.Write([]byte{0})
		}
		if contract[7] != hex.EncodeToString(routeDigest.Sum(nil)) {
			t.Errorf("form %q production route/handler binding drifted", form.id)
		}
		delete(contracts, form.id)
	}
	for id := range contracts {
		t.Errorf("stale form contract %q", id)
	}
}

func serializeProductionRoute(row productionFormRoute) string {
	return strings.Join([]string{row.method, row.route, row.registrationOwner, row.handlerOwner, strings.Join(row.formIDs, ",")}, "\t")
}

func discoverProductionFormMarkup(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("internal/webui/templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	markup := make(map[string]string)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for index, form := range formPattern.FindAll(body, -1) {
			markup[filepath.Base(path)+"#"+strconv.Itoa(index+1)] = string(form)
		}
	}
	return markup
}

func readProductionFormExclusions(t *testing.T) map[string]string {
	t.Helper()
	file, err := os.Open(productionFormExclusionsInventory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFormAuditFile(t, file)
	exclusions := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		columns := strings.Split(raw, "\t")
		if len(columns) != 3 {
			t.Fatalf("%s:%d columns = %d, want 3", productionFormExclusionsInventory, line, len(columns))
		}
		ordinal, err := strconv.Atoi(columns[1])
		if err != nil || ordinal < 1 {
			t.Fatalf("%s:%d invalid ordinal %q", productionFormExclusionsInventory, line, columns[1])
		}
		claim := columns[0] + "#" + columns[1]
		if _, duplicate := exclusions[claim]; duplicate {
			t.Fatalf("duplicate form exclusion %q", claim)
		}
		exclusions[claim] = columns[2]
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return exclusions
}
