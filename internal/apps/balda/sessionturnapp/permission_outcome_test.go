package sessionturnapp

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
)

func TestPermissionOutcomeTurnMessage(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome permissioncmd.Outcome
		want    string
	}{
		{name: "timeout", outcome: permissioncmd.Outcome{Kind: permissioncmd.OutcomeDenied, Source: "timeout"}, want: "Permission request timed out. The requested action was denied."},
		{name: "delivery", outcome: permissioncmd.Outcome{Kind: permissioncmd.OutcomeCanceled, Source: "delivery_failed"}, want: "The permission request could not be delivered. The requested action was denied."},
		{name: "user", outcome: permissioncmd.Outcome{Kind: permissioncmd.OutcomeDenied, Source: "user"}, want: "Permission denied. The requested action was not performed."},
		{name: "allowed", outcome: permissioncmd.Outcome{Kind: permissioncmd.OutcomeAllowed, Source: "user"}, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := permissionOutcomeTurnMessage(test.outcome, true); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPermissionOutcomeRecorderKeepsLatestDenialAcrossLaterAllow(t *testing.T) {
	recorder := &permissionOutcomeRecorder{}
	recorder.RecordPermissionOutcome(permissioncmd.Outcome{Kind: permissioncmd.OutcomeDenied, Source: "timeout"})
	recorder.RecordPermissionOutcome(permissioncmd.Outcome{Kind: permissioncmd.OutcomeAllowed, Source: "user"})
	outcome, ok := recorder.Latest()
	if !ok || outcome.Kind != permissioncmd.OutcomeDenied || outcome.Source != "timeout" {
		t.Fatalf("outcome = %+v ok=%v", outcome, ok)
	}
}
