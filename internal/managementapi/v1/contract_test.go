package managementv1

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

const managementPrefix = "/api/management/v1"

func TestManagementContract(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger() error = %v", err)
	}
	if err := spec.Validate(t.Context()); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if findings := inspectManagementContract(spec); len(findings) != 0 {
		t.Fatalf("management contract findings:\n%s", strings.Join(findings, "\n"))
	}
}

func TestManagementContractGuardRejectsInvalidFixtures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*openapi3.T)
		want   string
	}{
		{
			name: "missing conjunctive security",
			mutate: func(spec *openapi3.T) {
				spec.Paths.Find(managementPrefix + "/auth/refresh").Post.Security = nil
			},
			want: "refreshWebAuth security",
		},
		{
			name: "ambiguous browser session property",
			mutate: func(spec *openapi3.T) {
				spec.Components.Schemas["BrowserAuthority"] = openapi3.NewObjectSchema().
					WithProperty("session_id", openapi3.NewStringSchema()).NewRef()
			},
			want: "ambiguous property",
		},
		{
			name: "ambiguous session schema",
			mutate: func(spec *openapi3.T) {
				spec.Components.Schemas["SessionAuthority"] = openapi3.NewObjectSchema().
					WithProperty("web_auth_session_id", openapi3.NewStringSchema()).NewRef()
			},
			want: "ambiguous schema SessionAuthority",
		},
		{
			name: "unbounded error message",
			mutate: func(spec *openapi3.T) {
				spec.Components.Schemas["ManagementError"].Value.Properties["message"].Value.MaxLength = nil
			},
			want: "ManagementError.message maxLength",
		},
		{
			name: "unbounded credential response",
			mutate: func(spec *openapi3.T) {
				spec.Components.Schemas["WebAuthMFAFinishRequest"].Value.Properties["credential_response"].Value.MaxLength = nil
			},
			want: "WebAuthMFAFinishRequest.credential_response maxLength",
		},
		{
			name: "credential loses sensitivity marker",
			mutate: func(spec *openapi3.T) {
				delete(spec.Components.Schemas["WebAuthCredentials"].Value.Properties["access_token"].Value.Extensions, "x-sensitive")
			},
			want: "WebAuthCredentials.access_token x-sensitive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := GetSwagger()
			if err != nil {
				t.Fatalf("GetSwagger() error = %v", err)
			}
			test.mutate(spec)
			if findings := inspectManagementContract(spec); !strings.Contains(strings.Join(findings, "\n"), test.want) {
				t.Fatalf("findings = %v, want finding containing %q", findings, test.want)
			}
		})
	}
}

func TestGeneratedManagementRoundTrips(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	label := "synthetic"
	channel := "telegram"
	accessExpiry := now.Add(15 * time.Minute)
	refreshExpiry := now.Add(24 * time.Hour)
	tests := []struct {
		name  string
		value any
		new   func() any
	}{
		{
			name: "WebAuth principal",
			value: WebAuthPrincipal{
				UserID: "user-synthetic", Username: "synthetic", DisplayName: "Synthetic User",
				Role: Administrator, Assurance: Normal, AccessExpiresAt: accessExpiry, MFAEnabled: true,
				UserVersion: 2, CredentialVersion: 3, MFAVersion: 1,
				WebAuthSessionID: "web-auth-synthetic", WebAuthSessionVersion: 4,
			},
			new: func() any { return new(WebAuthPrincipal) },
		},
		{
			name: "management resource",
			value: ManagementResourceSummary{
				ResourceID: "resource-synthetic", ResourceVersion: 7, UpdatedAt: now,
			},
			new: func() any { return new(ManagementResourceSummary) },
		},
		{
			name: "WebAuth credentials",
			value: WebAuthCredentials{
				AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh",
				CSRFToken: "synthetic-csrf", Assurance: Normal,
				AccessExpiresAt: accessExpiry, RefreshExpiresAt: refreshExpiry,
			},
			new: func() any { return new(WebAuthCredentials) },
		},
		{
			name: "AI session",
			value: AISessionSummary{
				AISessionID: "ai-synthetic", Label: &label, ChannelType: &channel,
				Status: "active", ObservedAt: now,
			},
			new: func() any { return new(AISessionSummary) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			got := test.new()
			if err := json.Unmarshal(data, got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if !reflect.DeepEqual(reflect.ValueOf(got).Elem().Interface(), test.value) {
				t.Fatalf("round trip = %#v, want %#v", got, test.value)
			}
		})
	}
}

func TestGeneratedIdentitiesRemainDisjoint(t *testing.T) {
	aiJSON, err := json.Marshal(AISessionSummary{
		AISessionID: "same-raw-id", Status: "active", ObservedAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(AISessionSummary) error = %v", err)
	}
	if strings.Contains(string(aiJSON), "web_auth_session_id") {
		t.Fatalf("AI JSON contains WebAuth authority: %s", aiJSON)
	}
	var principal WebAuthPrincipal
	if err := json.Unmarshal(aiJSON, &principal); err != nil {
		t.Fatalf("Unmarshal(WebAuthPrincipal) error = %v", err)
	}
	if principal.WebAuthSessionID != "" {
		t.Fatalf("AI identity populated WebAuth authority: %#v", principal)
	}

	principalJSON, err := json.Marshal(WebAuthPrincipal{
		UserID: "user", Role: Operator, Assurance: Normal, UserVersion: 1,
		CredentialVersion: 1, WebAuthSessionID: "same-raw-id", WebAuthSessionVersion: 1,
	})
	if err != nil {
		t.Fatalf("Marshal(WebAuthPrincipal) error = %v", err)
	}
	if strings.Contains(string(principalJSON), "ai_session_id") {
		t.Fatalf("WebAuth JSON contains AI identity: %s", principalJSON)
	}
}

func TestManagementCompatibilityIdentity(t *testing.T) {
	compatible := ManagementCompatibility{
		ContractID:      BaldaManagementV1,
		ContractVersion: ContractVersion,
		APIMajor:        N1,
	}
	if string(compatible.ContractID) != ContractID || int(compatible.APIMajor) != APIMajor {
		t.Fatalf("compatibility = %#v, constants = (%q, %d)", compatible, ContractID, APIMajor)
	}

	mismatched := compatible
	mismatched.ContractID = ManagementCompatibilityContractID("other.management.v1")
	if string(mismatched.ContractID) == ContractID {
		t.Fatal("mismatched contract identity was accepted")
	}
}

func TestManagementErrorsDoNotCarryCredentialValues(t *testing.T) {
	errorValue := ManagementError{
		Code:    "request.invalid",
		Message: "synthetic safe message",
	}
	data, err := json.Marshal(errorValue)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "csrf_token", "operator_token", "password"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("error JSON contains credential field %q: %s", forbidden, data)
		}
	}
}

func inspectManagementContract(spec *openapi3.T) []string {
	var findings []string
	if spec.Info == nil || spec.Info.Version != ContractVersion {
		findings = append(findings, fmt.Sprintf("OpenAPI info.version must equal %q", ContractVersion))
	}
	for _, path := range spec.Paths.Keys() {
		if !strings.HasPrefix(path, managementPrefix+"/") {
			findings = append(findings, fmt.Sprintf("path %q is outside management v1", path))
		}
		if strings.Contains(path, "/health") || strings.Contains(path, "/ready") {
			findings = append(findings, fmt.Sprintf("operational probe path %q is not part of management v1", path))
		}
		for method, operation := range spec.Paths.Value(path).Operations() {
			if operation == nil {
				continue
			}
			if operation.OperationID == "" {
				findings = append(findings, fmt.Sprintf("%s %s has no operationId", method, path))
			}
			if operation.Responses == nil || operation.Responses.Len() == 0 {
				findings = append(findings, fmt.Sprintf("%s has no responses", operation.OperationID))
				continue
			}
			if !hasSuccessResponse(operation.Responses) {
				findings = append(findings, fmt.Sprintf("%s has no success response", operation.OperationID))
			}
			findings = append(findings, inspectErrorResponses(operation)...)
		}
	}

	expectedSecurity := map[string][]string{
		"getManagementCompatibility":      {"operatorServiceAuth"},
		"loginWebAuth":                    {"operatorServiceAuth"},
		"finishWebAuthLogin":              {"operatorServiceAuth"},
		"refreshWebAuth":                  {"csrf", "operatorServiceAuth", "webAuthRefresh"},
		"getWebAuthPrincipal":             {"operatorServiceAuth", "webAuthAccess"},
		"logoutWebAuth":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"replaceWebAuthPassword":          {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"getWebAuthMFAStatus":             {"operatorServiceAuth", "webAuthAccess"},
		"beginWebAuthMFAEnable":           {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"finishWebAuthMFAEnable":          {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"beginWebAuthMFAReplace":          {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"finishWebAuthMFAReplace":         {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"disableWebAuthMFA":               {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listWebAuthSessions":             {"operatorServiceAuth", "webAuthAccess"},
		"revokeWebAuthSession":            {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listProviders":                   {"operatorServiceAuth", "webAuthAccess"},
		"createProvider":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"updateProvider":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"selectProvider":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"deleteProvider":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listSchedules":                   {"operatorServiceAuth", "webAuthAccess"},
		"getSchedule":                     {"operatorServiceAuth", "webAuthAccess"},
		"createSchedule":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"updateSchedule":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"selectSchedule":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"deleteSchedule":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listScheduleRuns":                {"operatorServiceAuth", "webAuthAccess"},
		"runScheduleNow":                  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"getScheduleRun":                  {"operatorServiceAuth", "webAuthAccess"},
		"listAliases":                     {"operatorServiceAuth", "webAuthAccess"},
		"getAlias":                        {"operatorServiceAuth", "webAuthAccess"},
		"createAlias":                     {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"retargetAlias":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"deleteAlias":                     {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listWebhooks":                    {"operatorServiceAuth", "webAuthAccess"},
		"getWebhook":                      {"operatorServiceAuth", "webAuthAccess"},
		"createWebhook":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"updateWebhook":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"selectWebhook":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"deleteWebhook":                   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"rotateWebhookSecret":             {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"testWebhook":                     {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"listWebhookHistory":              {"operatorServiceAuth", "webAuthAccess"},
		"getWebhookHistory":               {"operatorServiceAuth", "webAuthAccess"},
		"listMCPConnections":              {"operatorServiceAuth", "webAuthAccess"},
		"createMCPConnection":             {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"probeMCPConnection":              {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"updateMCPConnection":             {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"probeMCPConnectionUpdate":        {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"selectMCPConnection":             {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"deleteMCPConnection":             {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"diagnoseMCPConnection":           {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"onboardMCPBrowserAuthorization":  {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"onboardMCPDeviceAuthorization":   {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"beginMCPBrowserAuthorization":    {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"beginMCPDeviceAuthorization":     {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"completeMCPBrowserAuthorization": {"operatorServiceAuth", "webAuthAccess"},
		"getMCPDeviceAuthorization":       {"operatorServiceAuth", "webAuthAccess"},
		"cancelMCPAuthorization":          {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"disconnectMCPAuthorization":      {"csrf", "operatorServiceAuth", "webAuthAccess"},
		"retryMCPAuthorization":           {"csrf", "operatorServiceAuth", "webAuthAccess"},
	}
	for _, path := range spec.Paths.Keys() {
		for _, operation := range spec.Paths.Value(path).Operations() {
			if operation == nil {
				continue
			}
			want, ok := expectedSecurity[operation.OperationID]
			if !ok {
				findings = append(findings, fmt.Sprintf("unexpected operation %s", operation.OperationID))
				continue
			}
			findings = append(findings, inspectSecurity(operation, want)...)
			delete(expectedSecurity, operation.OperationID)
		}
	}
	for operationID := range expectedSecurity {
		findings = append(findings, fmt.Sprintf("missing operation %s", operationID))
	}

	findings = append(findings, inspectIdentitySchemas(spec)...)
	findings = append(findings, inspectWebAuthSchemas(spec)...)
	findings = append(findings, inspectProviderSchemas(spec)...)
	findings = append(findings, inspectScheduleSchemas(spec)...)
	findings = append(findings, inspectWebhookSchemas(spec)...)
	findings = append(findings, inspectMCPSchemas(spec)...)
	findings = append(findings, inspectErrorSchema(spec)...)
	slices.Sort(findings)
	return findings
}

func inspectWebhookSchemas(spec *openapi3.T) []string {
	var findings []string
	secret := spec.Components.Schemas["WebhookSecretResult"]
	if secret == nil || secret.Value == nil || secret.Value.Properties["secret"] == nil ||
		secret.Value.Properties["secret"].Value == nil || secret.Value.Properties["secret"].Value.Extensions["x-sensitive"] != true {
		findings = append(findings, "WebhookSecretResult.secret must be sensitive")
	}
	for _, schemaName := range []string{"WebhookItem", "WebhookInventory", "WebhookHistorySummary", "WebhookHistoryPage"} {
		data, err := json.Marshal(spec.Components.Schemas[schemaName])
		if err != nil {
			findings = append(findings, fmt.Sprintf("marshal %s: %v", schemaName, err))
			continue
		}
		if strings.Contains(string(data), "secret") {
			findings = append(findings, schemaName+" exposes webhook secret")
		}
	}
	return findings
}

func inspectScheduleSchemas(spec *openapi3.T) []string {
	inventory := spec.Components.Schemas["ScheduleInventory"]
	if inventory == nil || inventory.Value == nil || inventory.Value.Properties["items"] == nil ||
		inventory.Value.Properties["items"].Value == nil || inventory.Value.Properties["items"].Value.Items == nil ||
		inventory.Value.Properties["items"].Value.Items.Ref != "#/components/schemas/ScheduleSummaryItem" {
		return []string{"ScheduleInventory must use content-free ScheduleSummaryItem"}
	}
	data, err := json.Marshal(spec.Components.Schemas["ScheduleSummaryItem"])
	if err != nil {
		return []string{fmt.Sprintf("marshal ScheduleInventory: %v", err)}
	}
	if strings.Contains(string(data), "content") {
		return []string{"ScheduleInventory exposes schedule content"}
	}
	return nil
}

func inspectMCPSchemas(spec *openapi3.T) []string {
	var findings []string
	value := spec.Components.Schemas["MCPValueEdit"]
	if value == nil || value.Value == nil || value.Value.Properties["value"] == nil || !value.Value.Properties["value"].Value.WriteOnly {
		findings = append(findings, "MCPValueEdit.value must be writeOnly")
	}
	definition := spec.Components.Schemas["MCPDefinition"]
	if definition == nil || definition.Value == nil || definition.Value.Properties["config_revision"] == nil || !definition.Value.Properties["config_revision"].Value.ReadOnly {
		findings = append(findings, "MCPDefinition.config_revision must be readOnly")
	}
	for _, schemaName := range []string{"MCPItem", "MCPInventory", "MCPDiagnostic"} {
		data, err := json.Marshal(spec.Components.Schemas[schemaName])
		if err != nil {
			findings = append(findings, fmt.Sprintf("marshal schema %s: %v", schemaName, err))
			continue
		}
		for _, forbidden := range []string{"client_secret", "protected_values", "access_token", "refresh_token"} {
			if strings.Contains(string(data), forbidden) {
				findings = append(findings, fmt.Sprintf("%s exposes protected property %s", schemaName, forbidden))
			}
		}
	}
	for schemaName, propertyName := range map[string]string{"MCPAuthorizationInput": "client_secret", "MCPBrowserCallback": "state"} {
		schema := spec.Components.Schemas[schemaName]
		if schema == nil || schema.Value == nil || schema.Value.Properties[propertyName] == nil || !schema.Value.Properties[propertyName].Value.WriteOnly {
			findings = append(findings, fmt.Sprintf("%s.%s must be writeOnly", schemaName, propertyName))
		}
	}
	statusData, err := json.Marshal(spec.Components.Schemas["MCPDeviceAuthorizationStatus"])
	if err != nil {
		findings = append(findings, err.Error())
	} else {
		for _, forbidden := range []string{"verification_uri", "user_code"} {
			if strings.Contains(string(statusData), forbidden) {
				findings = append(findings, "device status exposes "+forbidden)
			}
		}
	}
	return findings
}

func inspectProviderSchemas(spec *openapi3.T) []string {
	var findings []string
	for schemaName, propertyName := range map[string]string{
		"ProviderSecretEdit":      "value",
		"ProviderEnvironmentEdit": "values",
	} {
		schema := spec.Components.Schemas[schemaName]
		if schema == nil || schema.Value == nil {
			findings = append(findings, fmt.Sprintf("missing schema %s", schemaName))
			continue
		}
		property := schema.Value.Properties[propertyName]
		if property == nil || property.Value == nil || !property.Value.WriteOnly {
			findings = append(findings, fmt.Sprintf("%s.%s must be writeOnly", schemaName, propertyName))
		}
	}
	for _, schemaName := range []string{"ProviderItem", "ProviderInventory"} {
		data, err := json.Marshal(spec.Components.Schemas[schemaName])
		if err != nil {
			findings = append(findings, fmt.Sprintf("marshal schema %s: %v", schemaName, err))
			continue
		}
		for _, forbidden := range []string{"\"api_key\":", "protected_values", "environment_values"} {
			if strings.Contains(string(data), forbidden) {
				findings = append(findings, fmt.Sprintf("%s exposes protected property %s", schemaName, forbidden))
			}
		}
	}
	return findings
}

func inspectSecurity(operation *openapi3.Operation, want []string) []string {
	if operation.Security == nil || len(*operation.Security) != 1 {
		return []string{fmt.Sprintf("%s security must contain one conjunctive requirement", operation.OperationID)}
	}
	requirement := (*operation.Security)[0]
	got := slices.Sorted(maps.Keys(requirement))
	if !slices.Equal(got, want) {
		return []string{fmt.Sprintf("%s security = %v, want %v", operation.OperationID, got, want)}
	}
	for scheme, scopes := range requirement {
		if len(scopes) != 0 {
			return []string{fmt.Sprintf("%s security scheme %s has unexpected scopes", operation.OperationID, scheme)}
		}
	}
	return nil
}

func hasSuccessResponse(responses *openapi3.Responses) bool {
	for _, status := range responses.Keys() {
		if strings.HasPrefix(status, "2") {
			return true
		}
	}
	return false
}

func inspectErrorResponses(operation *openapi3.Operation) []string {
	var findings []string
	for _, status := range operation.Responses.Keys() {
		if strings.HasPrefix(status, "2") {
			continue
		}
		response := operation.Responses.Value(status)
		if response == nil || response.Value == nil {
			findings = append(findings, fmt.Sprintf("%s response %s is unresolved", operation.OperationID, status))
			continue
		}
		media, ok := response.Value.Content["application/json"]
		if !ok || media.Schema == nil || media.Schema.Ref != "#/components/schemas/ManagementError" {
			findings = append(findings, fmt.Sprintf("%s response %s does not use ManagementError", operation.OperationID, status))
		}
	}
	return findings
}

func inspectIdentitySchemas(spec *openapi3.T) []string {
	var findings []string
	required := []string{"AISessionSummary", "WebAuthPrincipal", "WebAuthSessionSummary"}
	for _, name := range required {
		if spec.Components.Schemas[name] == nil {
			findings = append(findings, fmt.Sprintf("missing schema %s", name))
		}
	}
	for name, schemaRef := range spec.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		if name == "Session" || name == "SessionSummary" || name == "SessionAuthority" {
			findings = append(findings, fmt.Sprintf("ambiguous schema %s", name))
		}
		for property := range schemaRef.Value.Properties {
			if property == "session_id" {
				findings = append(findings, fmt.Sprintf("schema %s has ambiguous property session_id", name))
			}
		}
	}
	return findings
}

func inspectWebAuthSchemas(spec *openapi3.T) []string {
	type boundedProperty struct {
		schema, property string
		max              uint64
		writeOnly        bool
	}
	properties := []boundedProperty{
		{schema: "LocalLoginRequest", property: "username", max: 256},
		{schema: "LocalLoginRequest", property: "password", max: 4096, writeOnly: true},
		{schema: "WebAuthBrowserContext", property: "browser_binding", max: 256, writeOnly: true},
		{schema: "WebAuthBrowserContext", property: "csrf_token", max: 4096, writeOnly: true},
		{schema: "WebAuthLoginMFAFinishRequest", property: "credential_response", max: 131072, writeOnly: true},
		{schema: "WebAuthPasswordReplaceRequest", property: "current_password", max: 4096, writeOnly: true},
		{schema: "WebAuthPasswordReplaceRequest", property: "new_password", max: 4096, writeOnly: true},
		{schema: "WebAuthMFABeginRequest", property: "password", max: 4096, writeOnly: true},
		{schema: "WebAuthMFAReplaceBeginRequest", property: "password", max: 4096, writeOnly: true},
		{schema: "WebAuthMFADisableRequest", property: "password", max: 4096, writeOnly: true},
		{schema: "WebAuthMFAFinishRequest", property: "credential_response", max: 131072, writeOnly: true},
	}
	var findings []string
	for _, check := range properties {
		schema := spec.Components.Schemas[check.schema]
		if schema == nil || schema.Value == nil {
			findings = append(findings, fmt.Sprintf("missing schema %s", check.schema))
			continue
		}
		property := schema.Value.Properties[check.property]
		if property == nil || property.Value == nil || property.Value.MaxLength == nil || *property.Value.MaxLength != check.max {
			findings = append(findings, fmt.Sprintf("%s.%s maxLength must be %d", check.schema, check.property, check.max))
			continue
		}
		if check.writeOnly && !property.Value.WriteOnly {
			findings = append(findings, fmt.Sprintf("%s.%s must be writeOnly", check.schema, check.property))
		}
	}
	credentials := spec.Components.Schemas["WebAuthCredentials"]
	if credentials == nil || credentials.Value == nil {
		return append(findings, "missing WebAuthCredentials schema")
	}
	for _, name := range []string{"access_token", "refresh_token", "csrf_token"} {
		property := credentials.Value.Properties[name]
		if property == nil || property.Value == nil || property.Value.MaxLength == nil || *property.Value.MaxLength != 4096 {
			findings = append(findings, fmt.Sprintf("WebAuthCredentials.%s maxLength must be 4096", name))
			continue
		}
		if sensitive, ok := property.Value.Extensions["x-sensitive"].(bool); !ok || !sensitive {
			findings = append(findings, fmt.Sprintf("WebAuthCredentials.%s x-sensitive must be true", name))
		}
	}
	for _, forbidden := range []string{"Provider", "IdentityProvider", "AuthAction", "CeremonyAction"} {
		if _, exists := spec.Components.Schemas[forbidden]; exists {
			findings = append(findings, fmt.Sprintf("generic auth schema %s is forbidden", forbidden))
		}
	}
	return findings
}

func inspectErrorSchema(spec *openapi3.T) []string {
	ref := spec.Components.Schemas["ManagementError"]
	if ref == nil || ref.Value == nil {
		return []string{"missing ManagementError schema"}
	}
	schema := ref.Value
	var findings []string
	if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has {
		findings = append(findings, "ManagementError must reject additional properties")
	}
	for property, max := range map[string]uint64{"code": 64, "message": 512, "request_id": 128} {
		value := schema.Properties[property]
		if value == nil || value.Value == nil || value.Value.MaxLength == nil || *value.Value.MaxLength != max {
			findings = append(findings, fmt.Sprintf("ManagementError.%s maxLength must be %d", property, max))
		}
	}
	fields := schema.Properties["fields"]
	if fields == nil || fields.Value == nil || fields.Value.MaxItems == nil || *fields.Value.MaxItems != 16 {
		findings = append(findings, "ManagementError.fields maxItems must be 16")
	}
	fieldRef := spec.Components.Schemas["ManagementErrorField"]
	if fieldRef == nil || fieldRef.Value == nil {
		findings = append(findings, "missing ManagementErrorField schema")
		return findings
	}
	if fieldRef.Value.AdditionalProperties.Has == nil || *fieldRef.Value.AdditionalProperties.Has {
		findings = append(findings, "ManagementErrorField must reject additional properties")
	}
	for property, max := range map[string]uint64{"field": 128, "code": 64} {
		value := fieldRef.Value.Properties[property]
		if value == nil || value.Value == nil || value.Value.MaxLength == nil || *value.Value.MaxLength != max {
			findings = append(findings, fmt.Sprintf("ManagementErrorField.%s maxLength must be %d", property, max))
		}
	}
	return findings
}
