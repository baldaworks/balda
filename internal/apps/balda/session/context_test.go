package session

import "testing"

func TestKindFamilyAndOwnerValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    Kind
		owner   string
		family  Family
		wantErr bool
	}{
		{name: "dm", kind: KindDM, family: FamilyConversation},
		{name: "chat", kind: KindChat, family: FamilyConversation},
		{name: "schedule", kind: KindSchedule, owner: "job-1", family: FamilyService},
		{name: "webhook", kind: KindWebhook, owner: "job-2", family: FamilyService},
		{name: "missing kind", owner: "job-3", wantErr: true},
		{name: "conversation owner", kind: KindDM, owner: "job-4", family: FamilyConversation, wantErr: true},
		{name: "missing service owner", kind: KindSchedule, family: FamilyService, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.kind.Family(); got != test.family {
				t.Fatalf("Family() = %q, want %q", got, test.family)
			}
			err := validateExecutionContext(test.kind, test.owner)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateExecutionContext() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestKindForConversation(t *testing.T) {
	t.Parallel()
	if got := KindForConversation(true); got != KindDM {
		t.Fatalf("KindForConversation(true) = %q, want %q", got, KindDM)
	}
	if got := KindForConversation(false); got != KindChat {
		t.Fatalf("KindForConversation(false) = %q, want %q", got, KindChat)
	}
}
