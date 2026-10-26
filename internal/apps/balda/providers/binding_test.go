package providers

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalBindingJSON(t *testing.T) {
	raw := ` {
		"Warnings": null,
		"Entries": [{"RevisionID":"revision-1","Source":"managed","ProviderID":"provider-1"}],
		"Reproducible": true,
		"RootProviderID": "provider-1"
	}`
	want := `{"RootProviderID":"provider-1","Entries":[{"ProviderID":"provider-1","Source":"managed","RevisionID":"revision-1"}],"Reproducible":true,"Warnings":null}`
	got, err := CanonicalBindingJSON(raw)
	if err != nil {
		t.Fatalf("CanonicalBindingJSON() error = %v", err)
	}
	if got != want {
		t.Fatalf("CanonicalBindingJSON() = %q, want %q", got, want)
	}
}

func TestCanonicalBindingJSONRejectsInvalidOrProtectedData(t *testing.T) {
	valid := `{"RootProviderID":"provider-1","Entries":[{"ProviderID":"provider-1","Source":"managed","RevisionID":"revision-1"}],"Reproducible":true,"Warnings":null}`
	tests := []struct {
		name string
		raw  string
	}{
		{"noncanonical legacy shape", `{"root_provider_id":"provider-1"}`},
		{"protected top-level field", strings.TrimSuffix(valid, `}`) + `,"api_key":"must-not-land"}`},
		{"protected entry field", strings.Replace(valid, `"RevisionID":"revision-1"`, `"RevisionID":"revision-1","api_key":"must-not-land"`, 1)},
		{"unknown field", strings.TrimSuffix(valid, `}`) + `,"extra":true}`},
		{"missing root entry", strings.Replace(valid, `"ProviderID":"provider-1"`, `"ProviderID":"provider-2"`, 1)},
		{"unknown source", strings.Replace(valid, `"Source":"managed"`, `"Source":"protected"`, 1)},
		{"empty revision", strings.Replace(valid, `"RevisionID":"revision-1"`, `"RevisionID":""`, 1)},
		{"duplicate provider", strings.Replace(valid, `]`, `,{"ProviderID":"provider-1","Source":"managed","RevisionID":"revision-2"}]`, 1)},
		{"trailing data", valid + `{}`},
		{"secret warning", strings.Replace(valid, `"Warnings":null`, `"Warnings":["api_key=must-not-land"]`, 1)},
		{"reproducibility mismatch", strings.Replace(valid, `"Reproducible":true`, `"Reproducible":false`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CanonicalBindingJSON(test.raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CanonicalBindingJSON() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCanonicalBindingJSONRequiresSourceExactWarnings(t *testing.T) {
	configured := `{"RootProviderID":"configured-1","Entries":[{"ProviderID":"configured-1","Source":"configured","RevisionID":"revision-1"}],"Reproducible":false,"Warnings":["provider configured-1 depends on configured external values"]}`
	managed := `{"RootProviderID":"managed-1","Entries":[{"ProviderID":"managed-1","Source":"managed","RevisionID":"revision-1"}],"Reproducible":false,"Warnings":["provider managed-1 uses a floating registry artifact"]}`
	tests := []struct {
		name string
		raw  string
	}{
		{"configured without warning", strings.Replace(configured, `false,"Warnings":["provider configured-1 depends on configured external values"]`, `true,"Warnings":null`, 1)},
		{"configured with managed warning", strings.Replace(configured, "depends on configured external values", "inherits external process environment", 1)},
		{"managed with configured warning", strings.Replace(managed, "uses a floating registry artifact", "depends on configured external values", 1)},
		{"managed with two distinct warnings", strings.Replace(managed, `"provider managed-1 uses a floating registry artifact"]`, `"provider managed-1 uses a floating registry artifact","provider managed-1 inherits external process environment"]`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CanonicalBindingJSON(test.raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CanonicalBindingJSON() error = %v, want ErrInvalid", err)
			}
		})
	}
}
