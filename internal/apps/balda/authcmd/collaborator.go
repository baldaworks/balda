package authcmd

import "time"

// Collaborator represents an authorized collaborator record.
type Collaborator struct {
	UserID    string    `json:"user_id"`
	Username  string    `json:"username,omitempty"`
	FirstName string    `json:"first_name,omitempty"`
	AddedBy   string    `json:"added_by"`
	AddedAt   time.Time `json:"added_at"`
}
