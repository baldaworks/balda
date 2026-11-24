package backoffice

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type rationalizationScope struct {
	path   string
	groups map[string][]string
}

// rationalizationScopes is the exact TASK-011 audit boundary: every top-level
// test in a Story-touched lifecycle test file must have one explicit retained
// evidence class. Adding a test to these files requires classifying its unique
// failure signal instead of silently growing overlapping coverage.
var rationalizationScopes = []rationalizationScope{
	{path: "internal/apps/backoffice/internal/webui/webui_test.go", groups: map[string][]string{
		"production-template": {"TestVerifyAssets", "TestRendererFullFragmentAndHistoryContracts", "TestRendererGroupsAuthorizedNavigationWithoutDuplicateAccount", "TestRendererAgentEscapesInstructionAndShowsEmptyValues", "TestRendererPreservesErrorStatusAndDoesNotCommitRejectedModel", "TestRespondMutation", "TestSafeViewProjectionUsesOneBindingAndFamilyLevelSessions", "TestAuditProjectionDoesNotRenderFreeFormReason", "TestAuditProjectionNamesManagedAliasOperation", "TestEmbeddedAssetsServeOffline", "TestRenderedAssetsUseContentVersions", "TestShellShowsViewerIndependentlyOfInspectedUser", "TestMutationDetailPathSafety", "TestAuditProjectionNamesFactOperation"},
		"mutation-feedback":   {"TestHTMXMutationFormsUseSharedAccessibleStatus"},
	}},
	{path: "internal/apps/backoffice/mcp_test.go", groups: map[string][]string{
		"handler-authorization": {"TestMCPRoutesRequireCurrentAdministrator", "TestMCPDiagnosticUsesProtectedServerAuthorityAndRetainsLastResult", "TestMCPFormsFenceAuthorityAndNeverReflectValues"},
		"handler-shape":         {"TestMCPInventoryUsesSafeSharedRendering", "TestMCPIntakePreservesTransportFieldsForOwnerValidation", "TestCommittedMCPCreateHasRecoveryLink"},
	}},
	{path: "internal/apps/backoffice/providers_test.go", groups: map[string][]string{
		"handler-authorization": {"TestProvidersBrowserManagementAndSecretBoundary", "TestProviderProtectedEditDistinguishesDeleteFromExplicitEmpty", "TestProviderTypeSpecificFormsRejectInapplicableFields"},
	}},
	{path: "internal/apps/backoffice/schedules_test.go", groups: map[string][]string{
		"handler-authorization": {"TestSchedulesBrowserManagement"},
		"handler-shape":         {"TestScheduleDefinitionSelectsReportDestination"},
	}},
	{path: "internal/apps/backoffice/webhooks_test.go", groups: map[string][]string{
		"handler-authorization": {"TestWebhooksCreateWithRealManagedRoutePolicy", "TestWebhooksTestPostAndHistory", "TestWebhooksBrowserManagement"},
		"handler-shape":         {"TestWebhooksShowCanonicalCallbackURL"},
	}},
	{path: "internal/apps/backoffice/mobile_tables_browser_test.go", groups: map[string][]string{
		"responsive-root-prefixed": {"TestMobileTablesLayoutBrowser"},
	}},
	{path: "internal/apps/backoffice/mutation_feedback_browser_test.go", groups: map[string][]string{
		"responsive-root-prefixed": {"TestMutationFeedbackBrowser"},
	}},
	{path: "internal/apps/backoffice/schedules_aliases_layout_browser_test.go", groups: map[string][]string{
		"responsive-root-prefixed": {"TestSchedulesAliasesLayoutBrowser"},
	}},
	{path: "internal/apps/balda/backoffice_mcp_browser_test.go", groups: map[string][]string{
		"authenticated-browser": {"TestBackofficeMCPRecorderKeepsOnlySanitizedProtocolFacts", "TestBackofficeMCPBrowserWorkflow"},
	}},
	{path: "internal/apps/balda/mcpbackofficeapp/operations_test.go", groups: map[string][]string{
		"adapter-boundary": {"TestOperationsPersistProtectedEditsAndProbeExactCandidate", "TestOperationsInventoryRecoveryReadsNeverLaunch"},
	}},
	{path: "internal/apps/balda/mcpmanage/definitions_test.go", groups: map[string][]string{
		"service-security":  {"TestAuthorizationSelectionRetainsRevisionSecretsAndFencesIdentity", "TestConfiguredAuthorizationCapturesReadOnlyProtectedRevision", "TestDefinitionEditsFenceVersionsPreserveSecretsAndRetainPins", "TestOrdinaryDefinitionEditCannotChangeWorkerIdentity", "TestProbeRejectsMissingAndRevokedAuthorityBeforeLaunching"},
		"service-failure":   {"TestCreateValidatesBeforeSavingAndSeparatesSavedFromReady", "TestPendingAuthorizationRetryFencesQueuedDefinitionAndAuthority", "TestDefinitionCommitRechecksAuthorityTimeAfterPublicationQueue", "TestConfiguredStdioInventoryUsesCurrentTransportReadiness", "TestHybridInventoryConflictsAndConfiguredReadOnly", "TestProbeAndPublicationErrorsDoNotExposeCredentialsOrClaimReadiness", "TestReenableRevalidatesCurrentProviderSelection"},
		"service-lifecycle": {"TestDeletedDefinitionInventoryDetailAndRecreate"},
	}},
	{path: "internal/apps/balda/providers/service_test.go", groups: map[string][]string{
		"service-security":  {"TestServiceCompilesOrderedMixedGraphWithoutExposingSecrets", "TestInventoryProjectsConfiguredAndManagedWithoutProtectedPayloads", "TestRecreateDoesNotInheritAPIKey", "TestRestoreRejectsUnavailableContractAndTampering", "TestProviderCredentialsReadV1WriteV2AndRejectMCPScope", "TestValidateCredentialsChecksEveryRetainedRevisionWithoutMutation"},
		"service-failure":   {"TestCreateRejectsConfiguredAndActiveProviderCollisions", "TestActiveConfiguredManagedCollisionIsInvalid", "TestConfiguredProviderOwnsDeletedManagedReconciliationTombstone", "TestServiceRejectsUnsafeManagedDefinitionsAndBrokenGraphs", "TestCompileRejectsSourceCollisionAndCycle"},
		"service-lifecycle": {"TestProviderLifecycleFiltersPassiveDetailAndRecreate", "TestServiceRestoresExactManagedRevisionAndRejectsChangedConfiguredDefinition", "TestManagedReproducibilityTracksInheritedEnvironment"},
	}},
	{path: "internal/apps/balda/scheduledjobs/management_test.go", groups: map[string][]string{
		"service-concurrency": {"TestManagementManualRunIsIdempotentAndHistoryIsSafe", "TestManagedScheduleManualRunFreezesAliasAndDuplicateSelection"},
		"service-failure":     {"TestManualRunWithMissingAliasRecordsFailureWithoutChangingCron", "TestManagementRejectsInvalidDefinitionAndConfigMutation", "TestManagementValidatesSuppliedPublicReportLocator"},
		"service-lifecycle":   {"TestManagementProjectsPrepublicationCompletionTime", "TestManagementRunProjectionShowsFrozenReportLocator", "TestManagementLifecyclePreservesDisabledSelectionAndArchive", "TestManagementCreatesScheduleWithoutReportLocator", "TestManagementClearsIdleRetainedSessionWhenFlagIsRemoved", "TestManagementRetainsCleanupIntentWhenIdleCleanupFails"},
	}},
	{path: "internal/apps/balda/webhookbackofficeapp/operations_test.go", groups: map[string][]string{
		"adapter-boundary":        {"TestPostUsesCurrentRouteAndFormKey"},
		"migration-compatibility": {"TestProjectHistoryKeepsOpaqueInputExact"},
	}},
	{path: "internal/apps/balda/webhookmanagement/service_test.go", groups: map[string][]string{
		"service-authorization": {"TestManagedRouteValidationAndAuthority", "TestConfigOwnedRouteIsReadOnly"},
		"service-failure":       {"TestConfiguredNamesRemainReservedForManagedCreate", "TestConfiguredRouteReconciliationRetainsDisabledDeclarations", "TestManagedNameIsImmutable", "TestDisabledConfigRequiresValidDefinition"},
		"service-lifecycle":     {"TestInventoryLifecycleFilters", "TestManagedRouteLifecycleAndOneTimeSecret"},
	}},
}

var rationalizationBackendOwners = map[string][]string{
	"internal/apps/balda/state/provider_sqlite_test.go": {
		"TestSQLiteProviderContract/EncryptedStateMixedFramesSurviveRestart",
		"TestSQLiteProviderContract/MCPConcurrentRecreateRetainsHistory",
		"TestSQLiteProviderContract/Provider_ManagedProviderState",
		"TestSQLiteProviderContract/Provider_ScheduleManagementAuthorityAndVersion",
		"TestSQLiteProviderContract/Provider_WebhookRouteManagement",
	},
	"internal/apps/balda/state/provider_postgres_test.go": {
		"TestPostgresProviderContract/EncryptedStateMixedFramesSurviveRestart",
		"TestPostgresProviderContract/MCPConcurrentRecreateRetainsHistory",
		"TestPostgresProviderContract/Provider_ManagedProviderState",
		"TestPostgresProviderContract/Provider_ScheduleManagementAuthorityAndVersion",
		"TestPostgresProviderContract/Provider_WebhookRouteManagement",
	},
}

type rationalizationRule struct {
	name, reason string
	match        func(path, test string) bool
}

var rationalizationPackageDirectories = []string{
	"internal/apps/backoffice",
	"internal/apps/balda/mcpbackofficeapp",
	"internal/apps/balda/mcpmanage",
	"internal/apps/balda/providers",
	"internal/apps/balda/schedulebackofficeapp",
	"internal/apps/balda/scheduledjobs",
	"internal/apps/balda/webhookbackofficeapp",
	"internal/apps/balda/webhookmanagement",
	"internal/apps/balda/state",
}

var rationalizationRemainingRules = []rationalizationRule{
	{name: "manifest-infrastructure", reason: "mechanically enforces the audit inventory; it is not product-contract evidence", match: exactPath("internal/apps/backoffice/test_rationalization_test.go")},
	{name: "out-of-scope-backoffice-security", reason: "browser identity, password, MFA, refresh, and session security are outside managed lifecycle CRUD", match: pathPrefix("internal/apps/backoffice/security/")},
	{name: "out-of-scope-backoffice-audit", reason: "audit read-model filtering is outside MCP/Provider/Schedule/Webhook lifecycle mutation", match: pathPrefix("internal/apps/backoffice/audit/")},
	{name: "out-of-scope-webui-projection", reason: "production-template projections not changed by the four audited lifecycle families", match: pathPrefix("internal/apps/backoffice/internal/webui/")},
	{name: "out-of-scope-backoffice-feature", reason: "Backoffice application, account, access, plugin, skill, hook, memory, alias, or archive contract outside the four lifecycle families", match: directDirectoryExcept("internal/apps/backoffice", "internal/apps/backoffice/test_rationalization_test.go")},
	{name: "protected-mcp-backoffice-authorization", reason: "MCP onboarding and authorization adapter boundary has a distinct security failure signal", match: pathPrefix("internal/apps/balda/mcpbackofficeapp/")},
	{name: "protected-mcp-authorization-runtime", reason: "MCP OAuth, credential, diagnostic, and concurrency contracts are not definition lifecycle duplicates", match: pathPrefix("internal/apps/balda/mcpmanage/")},
	{name: "protected-provider-binding", reason: "provider binding serialization and protected-data validation are separate from managed lifecycle", match: pathPrefix("internal/apps/balda/providers/")},
	{name: "protected-schedule-adapter", reason: "schedule Backoffice adapter call-shape evidence differs from management and storage policy", match: pathPrefix("internal/apps/balda/schedulebackofficeapp/")},
	{name: "protected-schedule-execution", reason: "dispatch, retry, finalization, restart, and cron concurrency differ from management CRUD", match: pathPrefix("internal/apps/balda/scheduledjobs/")},
	{name: "protected-webhook-adapter", reason: "webhook admission/history adapter behavior differs from management CRUD", match: pathPrefix("internal/apps/balda/webhookbackofficeapp/")},
	{name: "protected-webhook-management", reason: "webhook policy tests in the audited management package retain exact service-level diagnostics", match: pathPrefix("internal/apps/balda/webhookmanagement/")},
	{name: "protected-state-mcp", reason: "MCP store, grant, migration, and concurrency evidence is backend-specific", match: stateFilePattern(`(?i)mcp`)},
	{name: "protected-state-provider", reason: "provider persistence, publication, and migration evidence is backend-specific", match: stateFilePattern(`(?i)provider`)},
	{name: "protected-state-schedule", reason: "schedule definitions, runs, migrations, and restart evidence are backend-specific", match: stateFilePattern(`(?i)schedul`)},
	{name: "protected-state-webhook", reason: "webhook routes, history, admissions, and migrations are backend-specific", match: stateFilePattern(`(?i)webhook`)},
	{name: "out-of-scope-state-contract", reason: "state contract belongs to another product boundary and is not a lifecycle deletion candidate", match: stateOtherFile},
}

const rationalizationInventoryPath = "internal/apps/backoffice/testdata/test-rationalization-inventory.txt"

func TestLifecycleRationalizationInventoryIsComplete(t *testing.T) {
	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	const wantScopedTests = 85
	classified := 0
	for _, scope := range rationalizationScopes {
		expected := make(map[string]string)
		for group, names := range scope.groups {
			if group == "" || len(names) == 0 {
				t.Errorf("%s has an empty classification", scope.path)
			}
			for _, name := range names {
				if previous := expected[name]; previous != "" {
					t.Errorf("%s/%s classified as both %s and %s", scope.path, name, previous, group)
				}
				expected[name] = group
				classified++
			}
		}
		actual := parseTopLevelTests(t, filepath.Join(repositoryRoot, scope.path))
		for name := range actual {
			if expected[name] == "" {
				t.Errorf("%s/%s is inside TASK-011 scope but unclassified", scope.path, name)
			}
		}
		for name, group := range expected {
			if !actual[name] {
				t.Errorf("retained owner %s/%s (%s) disappeared", scope.path, name, group)
			}
		}
	}
	if classified != wantScopedTests {
		t.Errorf("classified tests = %d, want exact scoped inventory %d", classified, wantScopedTests)
	}
	validateBackendOwners(t, repositoryRoot)

	inventory, err := buildRationalizationInventory(repositoryRoot, rationalizationRemainingRules)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(filepath.Join(repositoryRoot, rationalizationInventoryPath))
	if err != nil {
		t.Fatalf("read exact rationalization inventory: %v\nresolved inventory:\n%s", err, inventory)
	}
	const wantFocusedTests = 447
	if err := compareRationalizationInventory(snapshot, inventory, wantFocusedTests); err != nil {
		t.Fatal(err)
	}

	t.Run("negative controls", func(t *testing.T) {
		t.Run("new file or test", func(t *testing.T) {
			changed := append(bytes.Clone(inventory), []byte("internal/apps/backoffice/new_test.go|TestNewLifecycle|out-of-scope-backoffice-feature\n")...)
			if compareRationalizationInventory(snapshot, changed, wantFocusedTests) == nil {
				t.Fatal("new test did not invalidate exact inventory")
			}
		})
		t.Run("wrong signature", func(t *testing.T) {
			if validateGoTestFunction([]byte("package sample\nfunc TestWrong(value string) {}"), "TestWrong") == nil {
				t.Fatal("wrong test signature accepted")
			}
		})
		t.Run("manifest and test deleted together", func(t *testing.T) {
			lines := nonemptyLines(inventory)
			shorter := []byte(strings.Join(lines[1:], "\n") + "\n")
			if compareRationalizationInventory(shorter, shorter, wantFocusedTests) == nil {
				t.Fatal("simultaneous owner and manifest deletion bypassed fixed inventory count")
			}
		})
		t.Run("fake Run", func(t *testing.T) {
			source := []byte("package sample\nimport \"testing\"\nfunc runProviderContract(t *testing.T) { helper := func() { t.Run(\"Fake\", func(t *testing.T) {}) }; _ = helper }\nfunc decoy(t *testing.T) { t.Run(\"AlsoFake\", func(t *testing.T) {}) }")
			if got := directProviderContractSubtests(source); len(got) != 0 {
				t.Fatalf("nested or foreign .Run accepted: %v", got)
			}
		})
		t.Run("missing backend runner call", func(t *testing.T) {
			source := []byte("package sample\nimport \"testing\"\nfunc TestSQLiteProviderContract(t *testing.T) {}")
			if backendEntrypointCallsRunner(source, "TestSQLiteProviderContract") {
				t.Fatal("backend entrypoint without runProviderContract accepted")
			}
		})
		t.Run("duplicate backend owner", func(t *testing.T) {
			if validateUniqueBackendOwners([]string{"sqlite/TestOwner/Subtest", "sqlite/TestOwner/Subtest"}, 2) == nil {
				t.Fatal("duplicate backend owner accepted")
			}
		})
		t.Run("overlap and unmatched", func(t *testing.T) {
			test := discoveredTest{path: "internal/apps/balda/state/mcp_store_test.go", name: "TestMCP"}
			overlap := append(slices.Clone(rationalizationRemainingRules), rationalizationRule{name: "duplicate", reason: "negative control", match: pathPrefix("internal/apps/balda/state/")})
			if _, err := classifyRemainingTest(test, overlap); err == nil {
				t.Fatal("overlapping patterns accepted")
			}
			if _, err := classifyRemainingTest(discoveredTest{path: "new/package/new_test.go", name: "TestNew"}, rationalizationRemainingRules); err == nil {
				t.Fatal("unmatched new package accepted")
			}
		})
	})
}

type discoveredTest struct {
	path, name string
}

func buildRationalizationInventory(repositoryRoot string, rules []rationalizationRule) ([]byte, error) {
	explicit := make(map[string]string)
	for _, scope := range rationalizationScopes {
		for group, names := range scope.groups {
			for _, name := range names {
				explicit[scope.path+"|"+name] = group
			}
		}
	}
	var tests []discoveredTest
	seenFiles := make(map[string]bool)
	for _, directory := range rationalizationPackageDirectories {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, directory), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if seenFiles[relative] {
				return nil
			}
			seenFiles[relative] = true
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil || !strings.HasPrefix(function.Name.Name, "Test") {
					continue
				}
				if err := validateGoTestFunction(source, function.Name.Name); err != nil {
					return errors.New(relative + "/" + function.Name.Name + ": " + err.Error())
				}
				tests = append(tests, discoveredTest{path: relative, name: function.Name.Name})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	lines := make([]string, 0, len(tests))
	for _, test := range tests {
		key := test.path + "|" + test.name
		classification := explicit[key]
		if classification == "" {
			var err error
			classification, err = classifyRemainingTest(test, rules)
			if err != nil {
				return nil, err
			}
		}
		lines = append(lines, key+"|"+classification)
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func classifyRemainingTest(test discoveredTest, rules []rationalizationRule) (string, error) {
	var matches []string
	for _, rule := range rules {
		if rule.reason == "" {
			return "", errors.New("classification " + rule.name + " has no contract reason")
		}
		if rule.match(test.path, test.name) {
			matches = append(matches, rule.name)
		}
	}
	if len(matches) != 1 {
		return "", errors.New(test.path + "/" + test.name + " matched classifications: " + strings.Join(matches, ", "))
	}
	return matches[0], nil
}

func compareRationalizationInventory(want, got []byte, count int) error {
	wantLines, gotLines := nonemptyLines(want), nonemptyLines(got)
	if len(gotLines) != count {
		return errors.New("resolved focused test inventory = " + strconv.Itoa(len(gotLines)) + ", want " + strconv.Itoa(count))
	}
	if !slices.Equal(wantLines, gotLines) {
		return errors.New("rationalization inventory changed; classify and review exact additions/removals\nwant:\n" + string(want) + "got:\n" + string(got))
	}
	return nil
}

func nonemptyLines(data []byte) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseTopLevelTests(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	tests := make(map[string]bool)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Test") {
			tests[function.Name.Name] = true
		}
	}
	return tests
}

func exactPath(want string) func(string, string) bool {
	return func(path, _ string) bool { return path == want }
}

func pathPrefix(prefix string) func(string, string) bool {
	return func(path, _ string) bool { return strings.HasPrefix(path, prefix) }
}

func directDirectoryExcept(directory string, excluded ...string) func(string, string) bool {
	return func(path, _ string) bool {
		return filepath.ToSlash(filepath.Dir(path)) == directory && !slices.Contains(excluded, path)
	}
}

func stateFilePattern(pattern string) func(string, string) bool {
	compiled := regexp.MustCompile(pattern)
	return func(path, _ string) bool {
		return filepath.ToSlash(filepath.Dir(path)) == "internal/apps/balda/state" && compiled.MatchString(filepath.Base(path))
	}
}

func stateOtherFile(path, _ string) bool {
	if filepath.ToSlash(filepath.Dir(path)) != "internal/apps/balda/state" {
		return false
	}
	return !regexp.MustCompile(`(?i)mcp|provider|schedul|webhook`).MatchString(filepath.Base(path))
}

func validateBackendOwners(t *testing.T, repositoryRoot string) {
	t.Helper()
	contractSource, err := os.ReadFile(filepath.Join(repositoryRoot, "internal/apps/balda/state/provider_contract_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	directSubtests := directProviderContractSubtests(contractSource)
	if len(directSubtests) == 0 {
		t.Fatal("runProviderContract has no direct t.Run contract cases")
	}
	var exactOwners []string
	for path, owners := range rationalizationBackendOwners {
		source, err := os.ReadFile(filepath.Join(repositoryRoot, path))
		if err != nil {
			t.Error(err)
			continue
		}
		for _, owner := range owners {
			exactOwners = append(exactOwners, path+"/"+owner)
			parts := strings.Split(owner, "/")
			if len(parts) != 2 {
				t.Errorf("backend owner %q must name an exact top-level test/subtest", owner)
				continue
			}
			if err := validateGoTestFunction(source, parts[0]); err != nil {
				t.Errorf("backend owner %s/%s: %v", path, owner, err)
			}
			if !backendEntrypointCallsRunner(source, parts[0]) {
				t.Errorf("backend owner %s/%s does not call runProviderContract", path, owner)
			}
			if !slices.Contains(directSubtests, parts[1]) {
				t.Errorf("backend owner %s/%s disappeared", path, owner)
			}
		}
	}
	if err := validateUniqueBackendOwners(exactOwners, 10); err != nil {
		t.Error(err)
	}
}

func directProviderContractSubtests(source []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), "provider_contract_test.go", source, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var names []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "runProviderContract" || function.Body == nil {
			continue
		}
		for _, statement := range function.Body.List {
			expression, ok := statement.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				continue
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			receiver, receiverOK := selector.X.(*ast.Ident)
			literal, literalOK := call.Args[0].(*ast.BasicLit)
			if receiverOK && receiver.Name == "t" && selector.Sel.Name == "Run" && literalOK {
				names = append(names, strings.Trim(literal.Value, `"`))
			}
		}
	}
	return names
}

func backendEntrypointCallsRunner(source []byte, name string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), "backend_test.go", source, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name || function.Body == nil {
			continue
		}
		for _, statement := range function.Body.List {
			expression, ok := statement.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			callee, calleeOK := call.Fun.(*ast.Ident)
			if calleeOK && callee.Name == "runProviderContract" {
				return true
			}
		}
	}
	return false
}

func validateUniqueBackendOwners(owners []string, want int) error {
	if len(owners) != want {
		return errors.New("backend owner count = " + strconv.Itoa(len(owners)) + ", want " + strconv.Itoa(want))
	}
	seen := make(map[string]bool)
	for _, owner := range owners {
		if seen[owner] {
			return errors.New("duplicate backend owner " + owner)
		}
		seen[owner] = true
	}
	return nil
}
