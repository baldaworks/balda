package scheduledjobs

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestModeResolverClassifiesQueuedScheduleWires(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	for _, record := range []state.ScheduledJobRecord{
		{JobID: "daily", Source: state.ScheduledJobSourceManaged, ScheduleSpec: "0 9 * * *", Content: "review",
			NextRunAt: time.Now().UTC().Add(time.Hour), Status: state.ScheduledJobStatusActive,
			SessionID: "tg-9001-0", ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`},
		{JobID: "wait-1", Source: state.ScheduledJobSourceInternal, ScheduleSpec: "@once", Content: "wake",
			NextRunAt: time.Now().UTC().Add(time.Hour), Status: state.ScheduledJobStatusActive,
			SessionID: "tg-9001-0", ChannelType: "telegram", AddressKey: "9001:0", AddressJSON: `{"chat_id":9001,"topic_id":0}`},
	} {
		if err := jobs.Upsert(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	resolver := modeResolver{jobs: jobs}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"daily", false}, {"wait-1", true}, {"removed-old-timer", true}} {
		got, err := resolver.IsOneShot(t.Context(), tc.id)
		if err != nil || got != tc.want {
			t.Fatalf("IsOneShot(%q) = %t, %v; want %t", tc.id, got, err, tc.want)
		}
	}
}
