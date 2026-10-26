package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBindingWarnings    = 64
	maxBindingWarningSize = 512
)

// CanonicalBindingJSON validates and canonically encodes a public provider binding.
// Binding contains provider identities only; protected provider configuration is not
// part of this schema and is rejected as an unknown field.
func CanonicalBindingJSON(raw string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var binding Binding
	if err := decoder.Decode(&binding); err != nil {
		return "", fmt.Errorf("%w: decode provider binding", ErrInvalid)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", err
	}
	if err := validateBinding(binding); err != nil {
		return "", err
	}
	if len(binding.Warnings) == 0 {
		binding.Warnings = nil
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("%w: encode provider binding", ErrInvalid)
	}
	return string(encoded), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing provider binding data", ErrInvalid)
	}
	return nil
}

func validateBinding(binding Binding) error {
	if err := validateID(binding.RootProviderID); err != nil {
		return err
	}
	if len(binding.Entries) == 0 || len(binding.Entries) > maxProviders {
		return fmt.Errorf("%w: provider binding entries exceed bounds", ErrInvalid)
	}
	entries := make(map[string]Source, len(binding.Entries))
	rootPresent := false
	for _, entry := range binding.Entries {
		if err := validateID(entry.ProviderID); err != nil {
			return err
		}
		if err := validateID(entry.RevisionID); err != nil {
			return fmt.Errorf("%w: invalid provider revision id", ErrInvalid)
		}
		switch entry.Source {
		case SourceConfigured, SourceManaged:
		default:
			return fmt.Errorf("%w: invalid provider binding source", ErrInvalid)
		}
		if _, duplicate := entries[entry.ProviderID]; duplicate {
			return fmt.Errorf("%w: duplicate provider binding entry", ErrInvalid)
		}
		entries[entry.ProviderID] = entry.Source
		rootPresent = rootPresent || entry.ProviderID == binding.RootProviderID
	}
	if !rootPresent {
		return fmt.Errorf("%w: provider binding root is missing", ErrInvalid)
	}
	if len(binding.Warnings) > maxBindingWarnings {
		return fmt.Errorf("%w: provider binding warnings exceed bounds", ErrInvalid)
	}
	warnings := make(map[string]struct{}, len(binding.Warnings))
	warningsByProvider := make(map[string]int, len(binding.Warnings))
	for _, warning := range binding.Warnings {
		providerID, ok := bindingWarningProvider(warning, entries)
		if !publicBindingText(warning, maxBindingWarningSize) || !ok {
			return fmt.Errorf("%w: invalid provider binding warning", ErrInvalid)
		}
		if _, duplicate := warnings[warning]; duplicate {
			return fmt.Errorf("%w: duplicate provider binding warning", ErrInvalid)
		}
		warnings[warning] = struct{}{}
		warningsByProvider[providerID]++
		if warningsByProvider[providerID] > 1 {
			return fmt.Errorf("%w: multiple provider binding warnings", ErrInvalid)
		}
	}
	for providerID, source := range entries {
		if source == SourceConfigured && warningsByProvider[providerID] != 1 {
			return fmt.Errorf("%w: configured provider binding warning is missing", ErrInvalid)
		}
	}
	if binding.Reproducible != (len(binding.Warnings) == 0) {
		return fmt.Errorf("%w: provider binding reproducibility mismatch", ErrInvalid)
	}
	return nil
}

func bindingWarningProvider(warning string, entries map[string]Source) (string, bool) {
	for id, source := range entries {
		suffixes := []string{
			" uses a floating registry artifact",
			" inherits external process environment",
		}
		if source == SourceConfigured {
			suffixes = []string{" depends on configured external values"}
		}
		for _, suffix := range suffixes {
			if warning == "provider "+id+suffix {
				return id, true
			}
		}
	}
	return "", false
}

func publicBindingText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
