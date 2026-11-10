package clienttest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestExerciseStopsAtIncompatibleContract(t *testing.T) {
	for _, test := range []struct {
		name       string
		contractID managementv1.ManagementCompatibilityContractID
		apiMajor   managementv1.ManagementCompatibilityAPIMajor
	}{
		{name: "contract", contractID: "other.management.v1", apiMajor: managementv1.N1},
		{name: "major", contractID: managementv1.BaldaManagementV1, apiMajor: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/management/v1/compatibility" {
					t.Fatalf("request after failed compatibility gate: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(managementv1.ManagementCompatibility{
					ContractID: test.contractID, ContractVersion: "99.0.0", APIMajor: test.apiMajor,
				})
			}))
			t.Cleanup(server.Close)
			client, err := managementv1.NewClientWithResponses(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Exercise(t.Context(), client, Input{}); err == nil || !strings.Contains(err.Error(), "incompatible") {
				t.Fatalf("Exercise() error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestCompatibilityAllowsSameMajorAdditions(t *testing.T) {
	for _, test := range []struct {
		name  string
		value managementv1.ManagementCompatibility
		want  bool
	}{
		{name: "current", value: managementv1.ManagementCompatibility{ContractID: managementv1.BaldaManagementV1, ContractVersion: managementv1.ContractVersion, APIMajor: managementv1.N1}, want: true},
		{name: "same major additive version", value: managementv1.ManagementCompatibility{ContractID: managementv1.BaldaManagementV1, ContractVersion: "99.0.0", APIMajor: managementv1.N1}, want: true},
		{name: "other contract", value: managementv1.ManagementCompatibility{ContractID: "other.management.v1", ContractVersion: managementv1.ContractVersion, APIMajor: managementv1.N1}},
		{name: "other major", value: managementv1.ManagementCompatibility{ContractID: managementv1.BaldaManagementV1, ContractVersion: managementv1.ContractVersion, APIMajor: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := compatible(test.value); got != test.want {
				t.Fatalf("compatible() = %t, want %t", got, test.want)
			}
		})
	}
}
