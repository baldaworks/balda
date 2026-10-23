package questions

import "testing"

func TestNormalizedResumeTarget(t *testing.T) {
	tests := []struct {
		target   string
		wantKind string
		wantKey  string
	}{
		{target: "session:session-1", wantKind: "session", wantKey: "session-1"},
		{target: "permission:review-1", wantKind: "permission", wantKey: "session-1"},
		{target: "goalkeeper:job-1", wantKind: "goalkeeper", wantKey: "job-1"},
		{target: "unknown:key"},
		{target: "malformed"},
	}
	for _, test := range tests {
		kind, key := normalizedResumeTarget("session-1", test.target)
		if kind != test.wantKind || key != test.wantKey {
			t.Fatalf("normalizedResumeTarget(%q) = %q, %q", test.target, kind, key)
		}
	}
}
