package goalkeepercmd_test

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/go-actorlayer"
)

const (
	systemTarget      = "system"
	systemQuestionKey = "question"
)

func TestJobEnvelope_Provenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		from            actorlayer.ActorAddress
		locator         deliverycmd.Locator
		transportUserID string
		wantTarget      string
		wantKey         string
	}{
		{
			name: "telegram provenance",
			from: actorlayer.ActorAddress{Target: "telegram", Key: "tg-user-1"},
			locator: deliverycmd.Locator{
				ChannelType: "telegram",
				AddressKey:  "101:0",
				SessionID:   "tg-101-0",
			},
			transportUserID: "tg-user-1",
			wantTarget:      "telegram",
			wantKey:         "tg-user-1",
		},
		{
			name: "slack provenance",
			from: actorlayer.ActorAddress{Target: "slackagent", Key: "U12345"},
			locator: deliverycmd.Locator{
				ChannelType: "slackagent",
				AddressKey:  "c:T1:C2",
				SessionID:   "sla-c-T1-C2",
			},
			transportUserID: "U12345",
			wantTarget:      "slackagent",
			wantKey:         "U12345",
		},
		{
			name: "zulip provenance",
			from: actorlayer.ActorAddress{Target: "zulip", Key: "42"},
			locator: deliverycmd.Locator{
				ChannelType: "zulip",
				AddressKey:  "stream-1:topic-2",
				SessionID:   "zul-s1-t2",
			},
			transportUserID: "42",
			wantTarget:      "zulip",
			wantKey:         "42",
		},
		{
			name: "from key derived from transportUserID when empty in from",
			from: actorlayer.ActorAddress{Target: "telegram"},
			locator: deliverycmd.Locator{
				ChannelType: "telegram",
				AddressKey:  "101:0",
				SessionID:   "tg-101-0",
			},
			transportUserID: "tg-user-fallback",
			wantTarget:      "telegram",
			wantKey:         "tg-user-fallback",
		},
		{
			name: "from key derived from locator addressKey when both from key and transportUserID empty",
			from: actorlayer.ActorAddress{Target: "zulip"},
			locator: deliverycmd.Locator{
				ChannelType: "zulip",
				AddressKey:  "stream-1:topic-2",
				SessionID:   "zul-s1-t2",
			},
			transportUserID: "",
			wantTarget:      "zulip",
			wantKey:         "stream-1:topic-2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env, err := goalkeepercmd.JobEnvelope(tc.from, tc.locator, "build feature", tc.transportUserID, 10)
			if err != nil {
				t.Fatalf("JobEnvelope() error = %v", err)
			}
			if got := env.From.Target; got != tc.wantTarget {
				t.Errorf("env.From.Target = %q, want %q", got, tc.wantTarget)
			}
			if got := env.From.Key; got != tc.wantKey {
				t.Errorf("env.From.Key = %q, want %q", got, tc.wantKey)
			}
			if env.To.Target != "goalkeeper" {
				t.Errorf("env.To.Target = %q, want goalkeeper", env.To.Target)
			}
		})
	}
}

func TestJobEnvelope_InvalidProvenance(t *testing.T) {
	t.Parallel()

	validLocator := deliverycmd.Locator{
		ChannelType: "telegram",
		AddressKey:  "101:0",
		SessionID:   "tg-101-0",
	}

	tests := []struct {
		name            string
		from            actorlayer.ActorAddress
		locator         deliverycmd.Locator
		transportUserID string
		wantErrSubstr   string
	}{
		{
			name:          "empty target rejected",
			from:          actorlayer.ActorAddress{Target: "", Key: "user-1"},
			locator:       validLocator,
			wantErrSubstr: "provenance target is required",
		},
		{
			name:          "whitespace target rejected",
			from:          actorlayer.ActorAddress{Target: "   ", Key: "user-1"},
			locator:       validLocator,
			wantErrSubstr: "provenance target is required",
		},
		{
			name:          "empty key rejected when no fallback",
			from:          actorlayer.ActorAddress{Target: "telegram", Key: ""},
			locator:       deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-101-0"},
			wantErrSubstr: "provenance key is required",
		},
		{
			name: "target mismatch with locator channel_type rejected",
			from: actorlayer.ActorAddress{Target: "telegram", Key: "user-1"},
			locator: deliverycmd.Locator{
				ChannelType: "zulip",
				AddressKey:  "stream-1:topic-2",
				SessionID:   "zul-s1-t2",
			},
			wantErrSubstr: "does not match locator channel_type",
		},
		{
			name: "empty locator session_id rejected",
			from: actorlayer.ActorAddress{Target: "telegram", Key: "user-1"},
			locator: deliverycmd.Locator{
				ChannelType: "telegram",
				AddressKey:  "101:0",
				SessionID:   "",
			},
			wantErrSubstr: "session_id is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := goalkeepercmd.JobEnvelope(tc.from, tc.locator, "test objective", tc.transportUserID, 5)
			if err == nil {
				t.Fatalf("JobEnvelope() expected error containing %q, got nil", tc.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("JobEnvelope() error = %v, want substring %q", err, tc.wantErrSubstr)
			}
		})
	}
}

func TestJobPayload_EncodeDecode(t *testing.T) {
	t.Parallel()

	original := goalkeepercmd.JobPayload{
		JobID: "goal-test-1",
		Locator: deliverycmd.Locator{
			ChannelType: "slackagent",
			AddressKey:  "c:T1:C2",
			SessionID:   "sla-c-T1-C2",
		},
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
			ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true},
		},
		Objective:       "complete story",
		TransportUserID: "U999",
		MaxIterations:   15,
	}

	raw, err := goalkeepercmd.EncodeJobPayload(original)
	if err != nil {
		t.Fatalf("EncodeJobPayload() error = %v", err)
	}

	decoded, err := goalkeepercmd.DecodeJobPayload(raw)
	if err != nil {
		t.Fatalf("DecodeJobPayload() error = %v", err)
	}

	if decoded.JobID != original.JobID {
		t.Errorf("decoded.JobID = %q, want %q", decoded.JobID, original.JobID)
	}
	if decoded.Locator.ChannelType != original.Locator.ChannelType {
		t.Errorf("decoded.Locator.ChannelType = %q, want %q", decoded.Locator.ChannelType, original.Locator.ChannelType)
	}
	if decoded.Objective != original.Objective {
		t.Errorf("decoded.Objective = %q, want %q", decoded.Objective, original.Objective)
	}
	if decoded.TransportUserID != original.TransportUserID {
		t.Errorf("decoded.TransportUserID = %q, want %q", decoded.TransportUserID, original.TransportUserID)
	}
	if decoded.MaxIterations != original.MaxIterations {
		t.Errorf("decoded.MaxIterations = %d, want %d", decoded.MaxIterations, original.MaxIterations)
	}
}

func TestResumeEnvelope(t *testing.T) {
	t.Parallel()

	payload := goalkeepercmd.JobPayload{
		JobID: "goal-resume-123",
		Locator: deliverycmd.Locator{
			ChannelType: "telegram",
			AddressKey:  "101:0",
			SessionID:   "tg-101-0",
		},
		Objective: "resume work",
	}

	env, err := goalkeepercmd.ResumeEnvelope(payload)
	if err != nil {
		t.Fatalf("ResumeEnvelope() error = %v", err)
	}
	if env.From.Target != systemTarget || env.From.Key != systemQuestionKey {
		t.Errorf("env.From = %v, want %s:%s", env.From, systemTarget, systemQuestionKey)
	}
	if env.To.Target != "goalkeeper" || env.To.Key != "goal-resume-123" {
		t.Errorf("env.To = %v, want goalkeeper:goal-resume-123", env.To)
	}

	// Empty JobID fails
	_, err = goalkeepercmd.ResumeEnvelope(goalkeepercmd.JobPayload{JobID: ""})
	if err == nil {
		t.Fatal("ResumeEnvelope() expected error on empty JobID, got nil")
	}
}

func TestQuestionAnsweredAndTimedOutEnvelopes(t *testing.T) {
	t.Parallel()

	answered, err := goalkeepercmd.QuestionAnsweredEnvelope("job-1", "q-1", "yes", "2026-09-07T00:00:00Z")
	if err != nil {
		t.Fatalf("QuestionAnsweredEnvelope() error = %v", err)
	}
	if answered.From.Target != systemTarget || answered.From.Key != systemQuestionKey {
		t.Errorf("answered.From = %v, want %s:%s", answered.From, systemTarget, systemQuestionKey)
	}

	timedOut, err := goalkeepercmd.QuestionTimedOutEnvelope("job-1", "q-1", "2026-09-07T00:05:00Z")
	if err != nil {
		t.Fatalf("QuestionTimedOutEnvelope() error = %v", err)
	}
	if timedOut.From.Target != systemTarget || timedOut.From.Key != systemQuestionKey {
		t.Errorf("timedOut.From = %v, want %s:%s", timedOut.From, systemTarget, systemQuestionKey)
	}

	// Empty job ID fails
	if _, err := goalkeepercmd.QuestionAnsweredEnvelope("", "q-1", "yes", ""); err == nil {
		t.Fatal("expected error on empty job ID")
	}
	// Empty question ID fails
	if _, err := goalkeepercmd.QuestionTimedOutEnvelope("job-1", "", ""); err == nil {
		t.Fatal("expected error on empty question ID")
	}
}
