package state

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPrepareSessionRuntimeMetadata(t *testing.T) {
	requested := " requested-model "
	record := SessionRecord{
		RuntimeGeneration: 7,
		RequestedModel:    &requested,
		EffectiveModel:    "public-model",
		RuntimeOutcome:    "context_preserved",
	}
	if generation, err := prepareSessionRuntimeMetadata(&record); err != nil || generation != 7 {
		t.Fatalf("prepareSessionRuntimeMetadata() = %d, %v", generation, err)
	}
	if record.RequestedModel == nil || *record.RequestedModel != "requested-model" {
		t.Fatalf("normalized requested model = %v", record.RequestedModel)
	}

	tests := []SessionRecord{
		{RuntimeGeneration: math.MaxUint64},
		{EffectiveModel: strings.Repeat("m", maxEffectiveModelBytes+1)},
		{EffectiveModel: "model\nprivate"},
		{RuntimeOutcome: strings.Repeat("o", maxRuntimeOutcomeBytes+1)},
		{RequestedModel: stringPointer("private\nsecret")},
	}
	for i := range tests {
		if _, err := prepareSessionRuntimeMetadata(&tests[i]); err == nil {
			t.Errorf("prepareSessionRuntimeMetadata(case %d) error = nil", i)
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestValidateRuntimeOperationRecord(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	valid := RuntimeOperationRecord{
		OperationID:               "operation-1",
		SessionID:                 "session-1",
		InputHash:                 strings.Repeat("a", 64),
		Kind:                      "model",
		State:                     RuntimeOperationPrepared,
		ExpectedArchiveID:         "archive-1",
		ExpectedGeneration:        2,
		TargetSnapshotID:          "snapshot-2",
		TargetProviderBindingJSON: ` {"Warnings":null,"Entries":[{"RevisionID":"revision-2","Source":"managed","ProviderID":"provider-2"}],"Reproducible":true,"RootProviderID":"provider-2"}`,
		TargetModel:               "model-2",
		TargetRequestedModel:      stringPointer("model-2"),
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := prepareRuntimeOperationRecord(&valid); err != nil {
		t.Fatalf("prepareRuntimeOperationRecord(valid) error = %v", err)
	}
	wantBinding := `{"RootProviderID":"provider-2","Entries":[{"ProviderID":"provider-2","Source":"managed","RevisionID":"revision-2"}],"Reproducible":true,"Warnings":null}`
	if valid.TargetProviderBindingJSON != wantBinding {
		t.Fatalf("canonical target binding = %q, want %q", valid.TargetProviderBindingJSON, wantBinding)
	}

	terminal := valid
	terminal.State = RuntimeOperationTerminal
	terminal.Outcome = "applied"
	terminal.TerminalAt = &now
	if err := prepareRuntimeOperationRecord(&terminal); err != nil {
		t.Fatalf("prepareRuntimeOperationRecord(terminal) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RuntimeOperationRecord)
	}{
		{"operation id", func(record *RuntimeOperationRecord) { record.OperationID = " operation" }},
		{"input hash", func(record *RuntimeOperationRecord) { record.InputHash = strings.Repeat("G", 64) }},
		{"state", func(record *RuntimeOperationRecord) { record.State = "recovery" }},
		{"generation", func(record *RuntimeOperationRecord) { record.ExpectedGeneration = math.MaxUint64 }},
		{"mismatched requested model", func(record *RuntimeOperationRecord) { record.TargetRequestedModel = stringPointer("other") }},
		{"provider binding array", func(record *RuntimeOperationRecord) { record.TargetProviderBindingJSON = `[]` }},
		{"noncanonical legacy binding", func(record *RuntimeOperationRecord) {
			record.TargetProviderBindingJSON = `{"root_provider_id":"provider-2"}`
		}},
		{"protected provider field", func(record *RuntimeOperationRecord) {
			record.TargetProviderBindingJSON = `{"RootProviderID":"provider-2","Entries":[{"ProviderID":"provider-2","Source":"managed","RevisionID":"revision-2"}],"Reproducible":true,"Warnings":null,"api_key":"must-not-land"}`
		}},
		{"unknown provider field", func(record *RuntimeOperationRecord) {
			record.TargetProviderBindingJSON = `{"RootProviderID":"provider-2","Entries":[{"ProviderID":"provider-2","Source":"managed","RevisionID":"revision-2","unknown":true}],"Reproducible":true,"Warnings":null}`
		}},
		{"provider binding oversized", func(record *RuntimeOperationRecord) {
			record.TargetProviderBindingJSON = `{"value":"` + strings.Repeat("x", maxRuntimeProviderBindingJSONBytes) + `"}`
		}},
		{"terminal time on prepared", func(record *RuntimeOperationRecord) { record.TerminalAt = &now }},
		{"missing created time", func(record *RuntimeOperationRecord) { record.CreatedAt = time.Time{} }},
		{"protected control text", func(record *RuntimeOperationRecord) { record.ReasonCode = "raw\nsecret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := prepareRuntimeOperationRecord(&record); err == nil {
				t.Fatal("prepareRuntimeOperationRecord() error = nil")
			}
		})
	}
}
