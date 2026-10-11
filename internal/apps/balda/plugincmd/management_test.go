package plugincmd

import (
	"strings"
	"testing"
)

func TestPluginMutationSelectors(t *testing.T) {
	for _, operation := range []Operation{Install, Upgrade, Enable, Disable, Rollback, Remove} {
		request := PluginMutation{Operation: operation, PluginName: "demo", ExpectedState: "observed", Confirm: true}
		switch operation {
		case Install:
			request.MarketplaceName, request.CandidateToken = "main", "candidate"
		case Upgrade:
			request.CandidateToken = "candidate"
		case Rollback:
			request.Revision = "revision"
		}
		if err := request.Validate(); err != nil {
			t.Errorf("%s valid request: %v", operation, err)
		}
		request.Revision = "irrelevant"
		if operation != Rollback && request.Validate() == nil {
			t.Errorf("%s accepted irrelevant revision", operation)
		}
	}
	if (PluginMutation{Operation: Enable, PluginName: "demo", ExpectedState: "state", MarketplaceName: "other"}).Validate() == nil {
		t.Error("enable accepted marketplace selector")
	}
}

func TestObservationBindsCompleteState(t *testing.T) {
	base := Observation{Identity: "demo", RecordDigest: Digest("install"), Generation: "generation", RevisionsDigest: Digest("revisions")}
	token := base.Token()
	if !base.Matches(token) {
		t.Fatal("observation did not match itself")
	}
	for _, changed := range []Observation{
		{Identity: "other", RecordDigest: base.RecordDigest, Generation: base.Generation, RevisionsDigest: base.RevisionsDigest},
		{Identity: base.Identity, RecordDigest: Digest("disabled/origin changed"), Generation: base.Generation, RevisionsDigest: base.RevisionsDigest},
		{Identity: base.Identity, RecordDigest: base.RecordDigest, Generation: "", RevisionsDigest: base.RevisionsDigest},
		{Identity: base.Identity, RecordDigest: base.RecordDigest, Generation: base.Generation, RevisionsDigest: Digest("unavailable")},
	} {
		if changed.Matches(token) {
			t.Errorf("changed observation matched: %+v", changed)
		}
	}
	if base.Matches("malformed") || base.Matches("") {
		t.Error("malformed observation matched")
	}
}

func TestMutationRejectsInvalidSelectors(t *testing.T) {
	valid := []PluginMutation{
		{Operation: Install, PluginName: "demo", MarketplaceName: "main", CandidateToken: "candidate", ExpectedState: "state"},
		{Operation: Upgrade, PluginName: "demo", CandidateToken: "candidate", ExpectedState: "state"},
		{Operation: Enable, PluginName: "demo", ExpectedState: "state"},
		{Operation: Disable, PluginName: "demo", ExpectedState: "state", Confirm: true},
		{Operation: Rollback, PluginName: "demo", ExpectedState: "state", Revision: "revision", Confirm: true},
		{Operation: Remove, PluginName: "demo", ExpectedState: "state", Confirm: true},
	}
	for _, base := range valid {
		for _, change := range []func(*PluginMutation){
			func(r *PluginMutation) { r.PluginName = "" }, func(r *PluginMutation) { r.ExpectedState = "" },
			func(r *PluginMutation) { r.Operation = "unknown" },
		} {
			r := base
			change(&r)
			if r.Validate() == nil {
				t.Errorf("accepted invalid request %+v", r)
			}
		}
		if base.Operation != Install {
			r := base
			r.MarketplaceName = "unexpected"
			if r.Validate() == nil {
				t.Errorf("%s accepted marketplace", base.Operation)
			}
		}
		if base.Operation != Install && base.Operation != Upgrade {
			r := base
			r.CandidateToken = "unexpected"
			if r.Validate() == nil {
				t.Errorf("%s accepted candidate", base.Operation)
			}
		}
		if base.Operation == Install || base.Operation == Upgrade {
			r := base
			r.CandidateToken = ""
			if r.Validate() == nil {
				t.Errorf("%s accepted missing candidate", base.Operation)
			}
		}
		if base.Operation == Install {
			r := base
			r.MarketplaceName = ""
			if r.Validate() == nil {
				t.Error("accepted missing marketplace")
			}
		}
		if base.Operation == Rollback {
			r := base
			r.Revision = ""
			if r.Validate() == nil {
				t.Error("accepted missing revision")
			}
		}
		if base.Operation == Disable || base.Operation == Rollback || base.Operation == Remove {
			r := base
			r.Confirm = false
			if r.Validate() == nil {
				t.Errorf("%s accepted missing confirmation", base.Operation)
			}
		}
	}
}

func TestObservationPrivateRecords(t *testing.T) {
	records := []string{
		`{"enabled":true,"origin":"https://user:secret@example.test/private","revision":"r1"}`,
		`{"enabled":false,"origin":"https://user:secret@example.test/private","revision":"r1"}`,
		`{"enabled":true,"origin":"/private/host/path","revision":"r1"}`,
		`null`,
	}
	seen := map[string]bool{}
	for _, raw := range records {
		o := Observation{Identity: "demo", RecordDigest: Digest(raw)}
		token := o.Token()
		if seen[token] {
			t.Fatal("distinct records have equal tokens")
		}
		seen[token] = true
		if strings.Contains(token, "secret") || strings.Contains(token, "private") || len(token) != 64 {
			t.Fatalf("unsafe token %q", token)
		}
	}
	o := Observation{Identity: "demo", RecordDigest: Digest(records[0])}
	before := o.Token()
	o.RevisionsDigest = Digest(`[{"revision":"r1","available":true}]`)
	if o.Matches(before) {
		t.Error("missing revision metadata matched available revision")
	}
	before = o.Token()
	o.RevisionsDigest = Digest(`[{"revision":"r1","available":false}]`)
	if o.Matches(before) {
		t.Error("changed revision availability matched")
	}
}
