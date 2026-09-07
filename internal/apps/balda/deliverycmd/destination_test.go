package deliverycmd

import (
	"testing"
)

func TestDestinationRecord_Validate(t *testing.T) {
	t.Parallel()

	validLoc, err := NewLocator("telegram", "123:0", `{"chat_id":123,"topic_id":0}`, "tg-123-0")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}

	tests := []struct {
		name    string
		dest    DestinationRecord
		wantErr bool
	}{
		{
			name: "valid record",
			dest: DestinationRecord{
				ChannelType: "telegram",
				Locator:     validLoc,
				Roles:       []string{RoleOwner},
				IsDefault:   true,
			},
			wantErr: false,
		},
		{
			name: "missing channel type",
			dest: DestinationRecord{
				Locator: validLoc,
			},
			wantErr: true,
		},
		{
			name: "channel type mismatch",
			dest: DestinationRecord{
				ChannelType: "slackagent",
				Locator:     validLoc,
			},
			wantErr: true,
		},
		{
			name: "empty locator address key",
			dest: DestinationRecord{
				ChannelType: "telegram",
				Locator: Locator{
					ChannelType: "telegram",
					SessionID:   "tg-123-0",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.dest.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDestinationRecord_HasRole(t *testing.T) {
	t.Parallel()

	dest := DestinationRecord{
		Roles: []string{"Owner", "collaborator"},
	}

	if !dest.HasRole("owner") {
		t.Errorf("expected HasRole(owner) = true")
	}
	if !dest.HasRole("OWNER") {
		t.Errorf("expected HasRole(OWNER) = true")
	}
	if !dest.HasRole("collaborator") {
		t.Errorf("expected HasRole(collaborator) = true")
	}
	if dest.HasRole("admin") {
		t.Errorf("expected HasRole(admin) = false")
	}
	if dest.HasRole("") {
		t.Errorf("expected HasRole(\"\") = false")
	}
}

func TestNormalizeRoles(t *testing.T) {
	t.Parallel()

	roles := []string{" OWNER ", "owner", "collaborator ", "COLLABORATOR", ""}
	got := NormalizeRoles(roles)
	if len(got) != 2 {
		t.Fatalf("NormalizeRoles() len = %d, want 2", len(got))
	}
	if got[0] != "owner" || got[1] != "collaborator" {
		t.Fatalf("NormalizeRoles() = %v, want [owner, collaborator]", got)
	}
}
