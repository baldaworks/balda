package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

const channelTelegram = "telegram"

// Preserve the identity namespace and fingerprint format of completed upgrades.
var userConversionNamespace = uuid.MustParse("90af3ae2-6775-50e3-b4fc-54fc0d011a55")

type conversionOwner struct {
	UserID       int64     `json:"user_id"`
	ChatID       int64     `json:"chat_id,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Bindings     []string  `json:"bindings,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

type userConversionInput struct {
	Owner         *conversionOwner
	Collaborators []authcmd.Collaborator
}

type preparedUserConversion struct {
	fingerprint      string
	sourceCountsJSON string
	primaryUserID    string
	users            []conversionUser
}

type conversionUser struct {
	user    usercmd.User
	binding usercmd.Binding
}

type conversionRecord struct {
	Subject     string `json:"subject"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Provenance  string `json:"provenance"`
	Username    string `json:"username,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
}

type conversionFingerprintRecord struct {
	Subject     string `json:"subject"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Provenance  string `json:"provenance"`
}

func prepareUserConversion(input userConversionInput) (preparedUserConversion, error) {
	if input.Owner == nil {
		return preparedUserConversion{}, fmt.Errorf("legacy owner is required")
	}
	records, ownerSubjects, err := userConversionRecords(input)
	if err != nil {
		return preparedUserConversion{}, err
	}
	if len(ownerSubjects) == 0 {
		return preparedUserConversion{}, fmt.Errorf("legacy owner has no valid subject")
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Subject < records[j].Subject })
	primarySubject := selectConversionPrimary(ownerSubjects)
	fingerprintRecords := make([]conversionFingerprintRecord, 0, len(records))
	for _, record := range records {
		fingerprintRecords = append(fingerprintRecords, conversionFingerprintRecord{
			Subject: record.Subject, Role: record.Role, DisplayName: record.DisplayName, Provenance: record.Provenance,
		})
	}
	encoded, err := json.Marshal(struct {
		Records        []conversionFingerprintRecord `json:"records"`
		PrimarySubject string                        `json:"primary_subject"`
	}{Records: fingerprintRecords, PrimarySubject: primarySubject})
	if err != nil {
		return preparedUserConversion{}, fmt.Errorf("encode legacy user snapshot: %w", err)
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	counts, _ := json.Marshal(map[string]int{"bindings": len(records), "users": len(records)})
	prepared := preparedUserConversion{fingerprint: fingerprint, sourceCountsJSON: string(counts)}
	usernames := make(map[string]string, len(records))
	for _, record := range records {
		channelType, principal, _ := parseConversionSubject(record.Subject)
		userID := userConversionID("user", record.Subject)
		bindingID := userConversionID("binding", record.Subject)
		role := usercmd.RoleOperator
		if record.Role == string(usercmd.RoleAdministrator) {
			role = usercmd.RoleAdministrator
		}
		username := conversionUsername(channelType, principal, record.Subject)
		if record.Subject == primarySubject {
			username = usercmd.PrimaryUsername
		}
		if prior, exists := usernames[username]; exists {
			return preparedUserConversion{}, fmt.Errorf("legacy subjects %q and %q produce the same username", prior, record.Subject)
		}
		usernames[username] = record.Subject
		user := usercmd.User{
			ID: userID, DisplayName: record.DisplayName, Username: username, NormalizedUsername: username,
			Status: usercmd.StatusActive, Role: role,
			Credential: usercmd.Credential{State: usercmd.CredentialStateDisabled, Version: 1},
			Primary:    record.Subject == primarySubject, Version: 1,
		}
		prepared.users = append(prepared.users, conversionUser{
			user: user,
			binding: usercmd.Binding{
				ID: bindingID, UserID: userID, ChannelType: channelType, Principal: principal,
				DisplayName: record.DisplayName, ProviderUsername: record.Username,
				ProviderFirstName: record.FirstName, Provenance: record.Provenance,
			},
		})
		if user.Primary {
			prepared.primaryUserID = user.ID
		}
	}
	return prepared, nil
}

func userConversionRecords(input userConversionInput) ([]conversionRecord, []string, error) {
	seen := make(map[string]string)
	var records []conversionRecord
	var ownerSubjects []string
	add := func(raw, role, displayName, provenance, username, firstName string) error {
		channelType, principal, err := parseConversionSubject(raw)
		if err != nil {
			return err
		}
		subject := channelType + ":" + principal
		if prior, ok := seen[subject]; ok {
			if prior != role {
				return fmt.Errorf("legacy principal %q has conflicting roles", subject)
			}
			return nil
		}
		seen[subject] = role
		if strings.TrimSpace(displayName) == "" {
			displayName = subject
		}
		if channelType != channelTelegram {
			username, firstName = "", ""
		}
		records = append(records, conversionRecord{
			Subject: subject, Role: role, DisplayName: displayName, Provenance: provenance,
			Username: strings.TrimSpace(username), FirstName: strings.TrimSpace(firstName),
		})
		if role == string(usercmd.RoleAdministrator) {
			ownerSubjects = append(ownerSubjects, subject)
		}
		return nil
	}
	ownerCandidates := append([]string{input.Owner.Subject}, input.Owner.Bindings...)
	if input.Owner.UserID != 0 {
		ownerCandidates = append(ownerCandidates, "telegram:"+strconv.FormatInt(input.Owner.UserID, 10))
	}
	ownerProvenance := "legacy-owner"
	if input.Owner.ChatID != 0 {
		ownerProvenance += ";chat_id=" + strconv.FormatInt(input.Owner.ChatID, 10)
	}
	if !input.Owner.RegisteredAt.IsZero() {
		ownerProvenance += ";registered_at=" + input.Owner.RegisteredAt.UTC().Format(time.RFC3339)
	}
	for _, subject := range ownerCandidates {
		if strings.TrimSpace(subject) == "" {
			continue
		}
		if err := add(subject, string(usercmd.RoleAdministrator), usercmd.PrimaryUsername, ownerProvenance, "", ""); err != nil {
			return nil, nil, err
		}
	}
	for _, collaborator := range input.Collaborators {
		displayName := strings.TrimSpace(strings.Join([]string{collaborator.FirstName, collaborator.Username}, " "))
		if err := add(collaborator.UserID, string(usercmd.RoleOperator), displayName, "legacy-collaborator", collaborator.Username, collaborator.FirstName); err != nil {
			return nil, nil, err
		}
	}
	return records, ownerSubjects, nil
}

func parseConversionSubject(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("legacy subject is empty")
	}
	channelType := channelTelegram
	principal := trimmed
	if before, after, ok := strings.Cut(trimmed, ":"); ok {
		channelType = strings.ToLower(strings.TrimSpace(before))
		principal = strings.TrimSpace(after)
	}
	if principal == "" || principal != strings.TrimSpace(principal) {
		return "", "", fmt.Errorf("legacy subject %q has no principal", raw)
	}
	switch channelType {
	case channelTelegram, "zulip":
		value, err := strconv.ParseInt(principal, 10, 64)
		if err != nil || value <= 0 {
			return "", "", fmt.Errorf("legacy subject %q has invalid numeric principal", raw)
		}
	case "slackagent":
		team, userID, ok := strings.Cut(principal, ":")
		if !ok || strings.TrimSpace(team) == "" || strings.TrimSpace(userID) == "" {
			return "", "", fmt.Errorf("legacy subject %q has invalid Slack principal", raw)
		}
		principal = strings.TrimSpace(team) + ":" + strings.TrimSpace(userID)
	default:
		return "", "", fmt.Errorf("legacy subject %q has unsupported channel", raw)
	}
	return channelType, principal, nil
}

func selectConversionPrimary(ownerSubjects []string) string {
	sorted := append([]string(nil), ownerSubjects...)
	sort.Strings(sorted)
	for _, subject := range sorted {
		if strings.HasPrefix(subject, "telegram:") {
			return subject
		}
	}
	return sorted[0]
}

func userConversionID(kind, subject string) string {
	return uuid.NewSHA1(userConversionNamespace, []byte(kind+":"+subject)).String()
}

func conversionUsername(channelType, principal, subject string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(principal) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			normalized.WriteRune(r)
		} else {
			normalized.WriteByte('-')
		}
	}
	base := strings.Trim(normalized.String(), "-._")
	if base == "" {
		base = "user"
	}
	if len(base) > 32 {
		base = base[:32]
	}
	sum := sha256.Sum256([]byte(subject))
	return channelType + "-" + base + "-" + hex.EncodeToString(sum[:4])
}
