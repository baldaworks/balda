// Package usermigration converts legacy owner and collaborator authorization
// state into canonical users without retaining a runtime fallback.
package usermigration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/google/uuid"
)

const manifestVersion = 1

var migrationNamespace = uuid.MustParse("90af3ae2-6775-50e3-b4fc-54fc0d011a55")

// LegacyOwner is the safe legacy owner state consumed by the explicit migration.
type LegacyOwner struct {
	UserID       int64
	ChatID       int64
	Subject      string
	Bindings     []string
	RegisteredAt time.Time
}

// Input contains the complete legacy snapshot and secure output target.
type Input struct {
	Owner             *LegacyOwner
	Collaborators     []authcmd.Collaborator
	PrimarySubject    string
	CredentialsOutput string
}

// Result reports non-secret migration outcome details.
type Result struct {
	Applied           bool
	SourceFingerprint string
	UserCount         int
	BindingCount      int
	PrimaryUserID     string
}

type migrationStore interface {
	UserMigrationApplied(ctx context.Context, sourceFingerprint string) (bool, error)
	ApplyUserMigration(ctx context.Context, migration usercmd.UserMigration) (bool, error)
}

// Migrator performs one deterministic legacy snapshot conversion.
type Migrator struct {
	store  migrationStore
	random io.Reader
	now    func() time.Time
	hash   func([]byte) (string, error)
}

// Option customizes deterministic migration dependencies for tests.
type Option func(*Migrator)

// WithRandom overrides the cryptographic random source.
func WithRandom(random io.Reader) Option {
	return func(m *Migrator) { m.random = random }
}

// WithClock overrides the migration clock.
func WithClock(now func() time.Time) Option {
	return func(m *Migrator) { m.now = now }
}

// WithHasher overrides password hashing while preserving plaintext boundaries.
func WithHasher(hash func([]byte) (string, error)) Option {
	return func(m *Migrator) { m.hash = hash }
}

// New creates an explicit legacy-user migrator.
func New(store migrationStore, options ...Option) (*Migrator, error) {
	if store == nil {
		return nil, fmt.Errorf("migration store is required")
	}
	migrator := &Migrator{store: store, random: rand.Reader, now: time.Now, hash: userpassword.Hash}
	for _, option := range options {
		option(migrator)
	}
	if migrator.random == nil || migrator.now == nil || migrator.hash == nil {
		return nil, fmt.Errorf("migration dependencies are required")
	}
	return migrator, nil
}

// Migrate writes or reuses an exclusive credential manifest, then atomically commits canonical users.
func (m *Migrator) Migrate(ctx context.Context, input Input) (Result, error) {
	prepared, err := prepare(input)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		SourceFingerprint: prepared.fingerprint,
		UserCount:         len(prepared.users),
		BindingCount:      len(prepared.users),
		PrimaryUserID:     prepared.primaryUserID,
	}
	applied, err := m.store.UserMigrationApplied(ctx, prepared.fingerprint)
	if err != nil {
		return Result{}, fmt.Errorf("check canonical user migration: %w", err)
	}
	if applied {
		return result, nil
	}
	manifest, err := m.loadOrCreateManifest(input.CredentialsOutput, prepared)
	if err != nil {
		return Result{}, err
	}
	completedAt := m.now().UTC()
	batch := usercmd.UserMigration{
		ID:                uuid.NewSHA1(migrationNamespace, []byte("migration:"+prepared.fingerprint)).String(),
		SourceFingerprint: prepared.fingerprint, SourceCountsJSON: prepared.sourceCountsJSON,
		PrimaryUserID: prepared.primaryUserID, CompletedAt: completedAt,
		GeneratedBindingCount: len(prepared.users),
	}
	passwords := make(map[string]string, len(manifest.Users))
	for _, credential := range manifest.Users {
		passwords[credential.UserID] = credential.TemporaryPassword
	}
	for _, candidate := range prepared.users {
		password := []byte(passwords[candidate.user.ID])
		hash, err := m.hash(password)
		for i := range password {
			password[i] = 0
		}
		if err != nil {
			return Result{}, fmt.Errorf("hash generated credential: %w", err)
		}
		user := candidate.user
		user.CreatedAt = completedAt
		user.UpdatedAt = completedAt
		binding := candidate.binding
		binding.CreatedAt = completedAt
		binding.UpdatedAt = completedAt
		batch.Users = append(batch.Users, usercmd.MigrationUser{
			User: user, Secret: usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, Binding: &binding,
			Audits: []usercmd.AuditEvent{
				migrationAudit(prepared.fingerprint, user.ID, usercmd.AuditTargetUser, completedAt),
				migrationAudit(prepared.fingerprint, binding.ID, usercmd.AuditTargetBinding, completedAt),
			},
		})
	}
	committed, err := m.store.ApplyUserMigration(ctx, batch)
	if err != nil {
		return Result{}, fmt.Errorf("commit canonical user migration: %w", err)
	}
	result.Applied = committed
	return result, nil
}

type preparedMigration struct {
	fingerprint      string
	sourceCountsJSON string
	primaryUserID    string
	users            []preparedUser
}

type preparedUser struct {
	subject string
	user    usercmd.User
	binding usercmd.Binding
}

type sourceRecord struct {
	Subject     string `json:"subject"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Provenance  string `json:"provenance"`
}

func prepare(input Input) (preparedMigration, error) {
	if input.Owner == nil {
		return preparedMigration{}, fmt.Errorf("legacy owner is required")
	}
	records, ownerSubjects, err := sourceRecords(input)
	if err != nil {
		return preparedMigration{}, err
	}
	if len(ownerSubjects) == 0 {
		return preparedMigration{}, fmt.Errorf("legacy owner has no valid subject")
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Subject < records[j].Subject })
	primarySubject, err := selectPrimary(ownerSubjects, input.PrimarySubject)
	if err != nil {
		return preparedMigration{}, err
	}
	encoded, err := json.Marshal(struct {
		Records        []sourceRecord `json:"records"`
		PrimarySubject string         `json:"primary_subject"`
	}{Records: records, PrimarySubject: primarySubject})
	if err != nil {
		return preparedMigration{}, fmt.Errorf("encode legacy user snapshot: %w", err)
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	counts, _ := json.Marshal(map[string]int{"bindings": len(records), "users": len(records)})
	prepared := preparedMigration{fingerprint: fingerprint, sourceCountsJSON: string(counts)}
	usernames := make(map[string]string, len(records))
	for _, record := range records {
		channelType, principal, _ := parseSubject(record.Subject)
		userID := deterministicID("user", record.Subject)
		bindingID := deterministicID("binding", record.Subject)
		role := usercmd.RoleOperator
		if record.Role == string(usercmd.RoleAdministrator) {
			role = usercmd.RoleAdministrator
		}
		username := deterministicUsername(channelType, principal, record.Subject)
		if prior, exists := usernames[username]; exists {
			return preparedMigration{}, fmt.Errorf("legacy subjects %q and %q produce the same username", prior, record.Subject)
		}
		usernames[username] = record.Subject
		user := usercmd.User{
			ID: userID, DisplayName: record.DisplayName, Username: username, NormalizedUsername: username,
			Status: usercmd.StatusActive, Role: role,
			Credential: usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true, Version: 1},
			Primary:    record.Subject == primarySubject, Version: 1,
		}
		prepared.users = append(prepared.users, preparedUser{
			subject: record.Subject,
			user:    user,
			binding: usercmd.Binding{
				ID: bindingID, UserID: userID, ChannelType: channelType, Principal: principal,
				DisplayName: record.DisplayName, Provenance: record.Provenance,
			},
		})
		if user.Primary {
			prepared.primaryUserID = user.ID
		}
	}
	return prepared, nil
}

func sourceRecords(input Input) ([]sourceRecord, []string, error) {
	seen := make(map[string]string)
	var records []sourceRecord
	var ownerSubjects []string
	add := func(raw, role, displayName, provenance string) error {
		channelType, principal, err := parseSubject(raw)
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
		records = append(records, sourceRecord{Subject: subject, Role: role, DisplayName: displayName, Provenance: provenance})
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
		if err := add(subject, string(usercmd.RoleAdministrator), "Legacy owner", ownerProvenance); err != nil {
			return nil, nil, err
		}
	}
	for _, collaborator := range input.Collaborators {
		displayName := strings.TrimSpace(strings.Join([]string{collaborator.FirstName, collaborator.Username}, " "))
		if err := add(collaborator.UserID, string(usercmd.RoleOperator), displayName, "legacy-collaborator"); err != nil {
			return nil, nil, err
		}
	}
	return records, ownerSubjects, nil
}

func parseSubject(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("legacy subject is empty")
	}
	channelType := "telegram"
	principal := trimmed
	if before, after, ok := strings.Cut(trimmed, ":"); ok {
		channelType = strings.ToLower(strings.TrimSpace(before))
		principal = strings.TrimSpace(after)
	}
	if principal == "" || principal != strings.TrimSpace(principal) {
		return "", "", fmt.Errorf("legacy subject %q has no principal", raw)
	}
	switch channelType {
	case "telegram", "zulip":
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

func selectPrimary(ownerSubjects []string, explicit string) (string, error) {
	sorted := append([]string(nil), ownerSubjects...)
	sort.Strings(sorted)
	if strings.TrimSpace(explicit) != "" {
		channelType, principal, err := parseSubject(explicit)
		if err != nil {
			return "", err
		}
		wanted := channelType + ":" + principal
		for _, subject := range sorted {
			if subject == wanted {
				return wanted, nil
			}
		}
		return "", fmt.Errorf("explicit primary subject %q is not an owner", wanted)
	}
	for _, subject := range sorted {
		if strings.HasPrefix(subject, "telegram:") {
			return subject, nil
		}
	}
	return sorted[0], nil
}

func deterministicID(kind, subject string) string {
	return uuid.NewSHA1(migrationNamespace, []byte(kind+":"+subject)).String()
}

func deterministicUsername(channelType, principal, subject string) string {
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

type credentialManifest struct {
	Version           int                  `json:"version"`
	SourceFingerprint string               `json:"source_fingerprint"`
	Users             []manifestCredential `json:"users"`
}

type manifestCredential struct {
	UserID            string `json:"user_id"`
	Username          string `json:"username"`
	TemporaryPassword string `json:"temporary_password"`
}

func (m *Migrator) loadOrCreateManifest(path string, prepared preparedMigration) (credentialManifest, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return credentialManifest{}, fmt.Errorf("credentials output path is required")
	}
	manifest, err := readManifest(trimmed)
	if err == nil {
		if err := validateManifest(manifest, prepared); err != nil {
			return credentialManifest{}, err
		}
		return manifest, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return credentialManifest{}, err
	}
	manifest = credentialManifest{Version: manifestVersion, SourceFingerprint: prepared.fingerprint}
	passwords := make(map[string]struct{}, len(prepared.users))
	for _, candidate := range prepared.users {
		var password string
		for attempts := 0; attempts < 4; attempts++ {
			password, err = m.generatePassword()
			if err != nil {
				return credentialManifest{}, err
			}
			if _, exists := passwords[password]; !exists {
				break
			}
			password = ""
		}
		if password == "" {
			return credentialManifest{}, fmt.Errorf("generate distinct temporary passwords")
		}
		passwords[password] = struct{}{}
		manifest.Users = append(manifest.Users, manifestCredential{
			UserID: candidate.user.ID, Username: candidate.user.Username, TemporaryPassword: password,
		})
	}
	if err := writeManifest(trimmed, manifest); err != nil {
		return credentialManifest{}, err
	}
	return manifest, nil
}

func (m *Migrator) generatePassword() (string, error) {
	buffer := make([]byte, 24)
	if _, err := io.ReadFull(m.random, buffer); err != nil {
		return "", fmt.Errorf("generate temporary password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func readManifest(path string) (credentialManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return credentialManifest{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return credentialManifest{}, fmt.Errorf("inspect credentials manifest: %w", err)
	}
	if info.Mode().Perm() != 0o600 {
		return credentialManifest{}, fmt.Errorf("credentials manifest permissions are %04o, want 0600", info.Mode().Perm())
	}
	var manifest credentialManifest
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return credentialManifest{}, fmt.Errorf("decode credentials manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return credentialManifest{}, fmt.Errorf("decode credentials manifest: trailing data")
	}
	return manifest, nil
}

func validateManifest(manifest credentialManifest, prepared preparedMigration) error {
	if manifest.Version != manifestVersion || manifest.SourceFingerprint != prepared.fingerprint || len(manifest.Users) != len(prepared.users) {
		return fmt.Errorf("credentials manifest does not match legacy snapshot")
	}
	byID := make(map[string]manifestCredential, len(manifest.Users))
	passwords := make(map[string]struct{}, len(manifest.Users))
	for _, credential := range manifest.Users {
		if credential.UserID == "" || credential.Username == "" || credential.TemporaryPassword == "" {
			return fmt.Errorf("credentials manifest contains an incomplete user")
		}
		if _, exists := byID[credential.UserID]; exists {
			return fmt.Errorf("credentials manifest contains duplicate users")
		}
		if _, exists := passwords[credential.TemporaryPassword]; exists {
			return fmt.Errorf("credentials manifest contains duplicate passwords")
		}
		byID[credential.UserID] = credential
		passwords[credential.TemporaryPassword] = struct{}{}
	}
	for _, candidate := range prepared.users {
		credential, ok := byID[candidate.user.ID]
		if !ok || credential.Username != candidate.user.Username {
			return fmt.Errorf("credentials manifest user set does not match legacy snapshot")
		}
	}
	return nil
}

func writeManifest(path string, manifest credentialManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials manifest: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create credentials manifest: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write credentials manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync credentials manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close credentials manifest: %w", err)
	}
	complete = true
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open credentials manifest directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync credentials manifest directory: %w", err)
	}
	return nil
}

func migrationAudit(fingerprint, targetID string, targetType usercmd.AuditTargetType, occurredAt time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID:     uuid.NewSHA1(migrationNamespace, []byte("audit:"+fingerprint+":"+string(targetType)+":"+targetID)).String(),
		Action: usercmd.AuditActionUserMigrated, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: targetType, TargetID: targetID, Reason: "legacy authorization migration",
		Source: "legacy-user-migration", CorrelationID: fingerprint, OccurredAt: occurredAt,
	}
}
