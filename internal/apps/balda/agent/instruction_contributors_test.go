package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testInstructionContributor struct {
	id      string
	content string
	err     error
	input   *SessionInstructionContext
}

func (c *testInstructionContributor) ID() string { return c.id }

func (c *testInstructionContributor) ContributeSessionInstruction(_ context.Context, input SessionInstructionContext) (string, error) {
	if c.input != nil {
		*c.input = input
	}
	return c.content, c.err
}

func TestAssembleSessionInstructionOrdersContributorsAndPassesPinnedContext(t *testing.T) {
	t.Parallel()

	var captured SessionInstructionContext
	input := SessionInstructionContext{SnapshotID: "snapshot-1", SessionID: "session-1"}
	got, err := assembleSessionInstruction(context.Background(), "base", input, []SessionInstructionContributor{
		&testInstructionContributor{id: "zeta", content: "last"},
		&testInstructionContributor{id: "alpha", content: "first", input: &captured},
	})
	if err != nil {
		t.Fatalf("assembleSessionInstruction() error = %v", err)
	}
	if strings.Index(got, "[alpha]") > strings.Index(got, "[zeta]") {
		t.Fatalf("instruction contributor order is not deterministic:\n%s", got)
	}
	if captured.SnapshotID != input.SnapshotID || captured.SessionID != input.SessionID {
		t.Fatalf("contributor input = %+v, want %+v", captured, input)
	}
}

func TestAssembleSessionInstructionRejectsInvalidContributions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		contributors []SessionInstructionContributor
		want         string
	}{
		{
			name: "duplicate id",
			contributors: []SessionInstructionContributor{
				&testInstructionContributor{id: "same"},
				&testInstructionContributor{id: "same"},
			},
			want: "duplicate session instruction contributor id",
		},
		{
			name:         "invalid id",
			contributors: []SessionInstructionContributor{&testInstructionContributor{id: "Not Valid"}},
			want:         "invalid session instruction contributor id",
		},
		{
			name:         "contributor error",
			contributors: []SessionInstructionContributor{&testInstructionContributor{id: "broken", err: errors.New("boom")}},
			want:         `contribute session instruction "broken": boom`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := assembleSessionInstruction(context.Background(), "base", SessionInstructionContext{}, test.contributors)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("assembleSessionInstruction() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAssembleSessionInstructionEnforcesTotalBudget(t *testing.T) {
	t.Parallel()

	_, err := assembleSessionInstruction(context.Background(), "base", SessionInstructionContext{}, []SessionInstructionContributor{
		&testInstructionContributor{id: "large", content: strings.Repeat("x", maxSessionInstructionBytes)},
	})
	if err == nil || !strings.Contains(err.Error(), "session instruction exceeds size limit") {
		t.Fatalf("assembleSessionInstruction() error = %v, want size limit", err)
	}
}
