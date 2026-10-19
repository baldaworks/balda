package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/google/uuid"
)

const (
	maxProviders = 64
	maxDepth     = 16
	maxIDLength  = 128
)

type protectedValues struct {
	APIKey      string            `json:"api_key,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type Service struct {
	configured map[string]agentconfig.Config
	store      Store
	protector  *secrets.Protector
	registry   *agentconfig.DriverRegistry
	now        func() time.Time
	newID      func() string
}

func New(configured map[string]agentconfig.Config, store Store, protector *secrets.Protector) *Service {
	return &Service{
		configured: cloneConfigs(configured), store: store, protector: protector,
		registry: agentconfig.BuiltinDriverRegistry(), now: time.Now, newID: uuid.NewString,
	}
}

// Inventory returns configured and managed definitions without protected values.
func (s *Service) Inventory(ctx context.Context) (Inventory, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return Inventory{}, err
	}
	items := make([]Item, 0, len(s.configured)+len(state.Records))
	for id, cfg := range s.configured {
		descriptor, ok := s.registry.Lookup(cfg.Type)
		if !ok {
			return Inventory{}, fmt.Errorf("%w: unsupported configured provider type", ErrInvalid)
		}
		fingerprint, fingerprintErr := configuredFingerprint(cfg)
		if fingerprintErr != nil {
			return Inventory{}, fingerprintErr
		}
		public := cloneConfig(cfg)
		names := configEnvironmentNames(public)
		redact(&public)
		items = append(items, Item{Source: SourceConfigured,
			Record: Record{ID: id, CurrentRevisionID: fingerprint, Enabled: true},
			Revision: Revision{ID: fingerprint, ProviderID: id, DriverType: descriptor.Type,
				ContractVersion: descriptor.ContractVersion, FormatVersion: StorageFormatVersion,
				Definition: Definition{Config: public, HasAPIKey: configuredHasAPIKey(cfg), EnvironmentNames: names}},
		})
	}
	for _, record := range state.Records {
		if _, collision := s.configured[record.ID]; collision {
			return Inventory{}, fmt.Errorf("%w: provider source collision", ErrInvalid)
		}
		revision := findRevision(state.Revisions, record.ID, record.CurrentRevisionID)
		if revision == nil {
			return Inventory{}, ErrUnavailable
		}
		publicRevision := *revision
		publicRevision.ProtectedValues = nil
		items = append(items, Item{Source: SourceManaged, Record: record, Revision: publicRevision})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Record.ID < items[j].Record.ID })
	return Inventory{Version: state.Version, Items: items}, nil
}

func configuredHasAPIKey(cfg agentconfig.Config) bool {
	return cfg.OpenAI != nil && cfg.OpenAI.APIKey != "" || cfg.AIStudio != nil && cfg.AIStudio.APIKey != ""
}

// Create validates and persists one immutable initial revision.
func (s *Service) Create(ctx context.Context, change Change) (State, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return State{}, err
	}
	if state.Version != change.ExpectedVersion {
		return State{}, ErrConflict
	}
	if _, exists := s.configured[change.ID]; exists || findRecord(state.Records, change.ID) != nil {
		return State{}, fmt.Errorf("%w: provider id already exists", ErrInvalid)
	}
	revision, err := s.makeRevision(change.ID, change.Config, change.Protected, nil)
	if err != nil {
		return State{}, err
	}
	record := Record{ID: change.ID, CurrentRevisionID: revision.ID, Enabled: true}
	if err := s.validateCandidate(state, record, revision); err != nil {
		return State{}, err
	}
	return s.store.Apply(ctx, Mutation{ExpectedVersion: change.ExpectedVersion, Record: record, Revision: &revision, Audit: providerAudit(change.Audit, change.ID)})
}

// Update creates a revision and preserves immutable driver identity.
func (s *Service) Update(ctx context.Context, change Change) (State, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return State{}, err
	}
	if state.Version != change.ExpectedVersion {
		return State{}, ErrConflict
	}
	record := findRecord(state.Records, change.ID)
	current := findRevision(state.Revisions, change.ID, recordRevisionID(record))
	if record == nil || current == nil || record.Deleted {
		return State{}, ErrUnavailable
	}
	revision, err := s.makeRevision(change.ID, change.Config, change.Protected, current)
	if err != nil {
		return State{}, err
	}
	if immutableIdentity(*current) != immutableIdentity(revision) {
		return State{}, fmt.Errorf("%w: immutable provider identity changed", ErrInvalid)
	}
	next := *record
	next.CurrentRevisionID = revision.ID
	if err := s.validateCandidate(state, next, revision); err != nil {
		return State{}, err
	}
	return s.store.Apply(ctx, Mutation{ExpectedVersion: change.ExpectedVersion, Record: next, Revision: &revision, Audit: providerAudit(change.Audit, change.ID)})
}

// SetEnabled changes lifecycle state without rewriting a revision.
func (s *Service) SetEnabled(ctx context.Context, expected StateVersion, id string, enabled bool, audit usercmd.AuditEvent) (State, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return State{}, err
	}
	if state.Version != expected {
		return State{}, ErrConflict
	}
	record := findRecord(state.Records, id)
	if record == nil || record.Deleted {
		return State{}, ErrUnavailable
	}
	next := *record
	next.Enabled = enabled
	if err := s.validateLifecycle(state, next); err != nil {
		return State{}, err
	}
	return s.store.Apply(ctx, Mutation{ExpectedVersion: expected, Record: next, Audit: providerAudit(audit, id)})
}

// Delete tombstones a provider while retaining all revisions.
func (s *Service) Delete(ctx context.Context, expected StateVersion, id string, audit usercmd.AuditEvent) (State, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return State{}, err
	}
	if state.Version != expected {
		return State{}, ErrConflict
	}
	record := findRecord(state.Records, id)
	if record == nil || record.Deleted {
		return State{}, ErrUnavailable
	}
	next := *record
	next.Enabled = false
	next.Deleted = true
	if err := s.validateLifecycle(state, next); err != nil {
		return State{}, err
	}
	return s.store.Apply(ctx, Mutation{ExpectedVersion: expected, Record: next, Audit: providerAudit(audit, id)})
}

// CompileCurrent builds a passive graph from configured and current managed state.
func (s *Service) CompileCurrent(ctx context.Context, root string) (Compiled, error) {
	state, err := s.store.Read(ctx)
	if err != nil {
		return Compiled{}, err
	}
	return s.compile(root, state, nil)
}

// Restore loads exactly the revisions named by a binding.
func (s *Service) Restore(ctx context.Context, binding Binding) (Compiled, error) {
	refs := make([]RevisionRef, 0, len(binding.Entries))
	for _, entry := range binding.Entries {
		if entry.Source == SourceManaged {
			refs = append(refs, RevisionRef{ProviderID: entry.ProviderID, RevisionID: entry.RevisionID})
		}
	}
	revisions, err := s.store.Revisions(ctx, refs)
	if err != nil {
		return Compiled{}, err
	}
	state := State{Revisions: revisions}
	for _, entry := range binding.Entries {
		if entry.Source == SourceManaged {
			state.Records = append(state.Records, Record{ID: entry.ProviderID, CurrentRevisionID: entry.RevisionID, Enabled: true})
		}
	}
	compiled, err := s.compile(binding.RootProviderID, state, &binding)
	if err != nil {
		return Compiled{}, err
	}
	return compiled, nil
}

func (s *Service) makeRevision(id string, cfg agentconfig.Config, edit ProtectedEdit, current *Revision) (Revision, error) {
	if err := validateID(id); err != nil {
		return Revision{}, err
	}
	descriptor, ok := s.registry.Lookup(cfg.Type)
	if !ok || descriptor.ContractVersion == 0 || descriptor.Class == agentconfig.DriverClassACP && descriptor.Type == agentconfig.AgentTypeGenericACP {
		return Revision{}, fmt.Errorf("%w: unsupported managed provider type", ErrInvalid)
	}
	if err := rejectExplicitCommand(cfg); err != nil {
		return Revision{}, err
	}
	if containsPlaintext(cfg) {
		return Revision{}, fmt.Errorf("%w: protected values must use protected edits", ErrInvalid)
	}
	values, err := s.mergeProtected(current, edit)
	if err != nil {
		return Revision{}, err
	}
	if err := validateProtectedValues(descriptor.Class, values); err != nil {
		return Revision{}, err
	}
	public := cloneConfig(cfg)
	public.Type = descriptor.Type
	redact(&public)
	if _, err := agentconfig.NormalizeConfig(reconstruct(public, values), ""); err != nil {
		return Revision{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	revision := Revision{
		ID: s.newID(), ProviderID: id, DriverType: descriptor.Type,
		ContractVersion: descriptor.ContractVersion, FormatVersion: StorageFormatVersion,
		Definition: Definition{Config: public, HasAPIKey: values.APIKey != "", EnvironmentNames: sortedKeys(values.Environment), Reproducible: managedReproducible(public, descriptor)},
		CreatedAt:  s.now().UTC(),
	}
	if values.APIKey != "" || len(values.Environment) > 0 {
		payload, marshalErr := json.Marshal(values)
		if marshalErr != nil {
			return Revision{}, ErrInvalid
		}
		revision.ProtectedValues, err = s.protector.Seal(payload, revisionAAD(id, revision.ID))
		if err != nil {
			return Revision{}, ErrUnavailable
		}
	}
	return revision, nil
}

func (s *Service) mergeProtected(current *Revision, edit ProtectedEdit) (protectedValues, error) {
	values := protectedValues{Environment: map[string]string{}}
	if current != nil {
		var err error
		values, err = s.open(*current)
		if err != nil {
			return protectedValues{}, err
		}
	}
	if edit.APIKey != nil {
		values.APIKey = *edit.APIKey
	}
	for name, value := range edit.Environment {
		if value == nil {
			delete(values.Environment, name)
		} else {
			values.Environment[name] = *value
		}
	}
	return values, nil
}

func (s *Service) open(revision Revision) (protectedValues, error) {
	values := protectedValues{Environment: map[string]string{}}
	if len(revision.ProtectedValues) == 0 {
		return values, nil
	}
	payload, err := s.protector.Open(revision.ProtectedValues, revisionAAD(revision.ProviderID, revision.ID))
	if err != nil || json.Unmarshal(payload, &values) != nil {
		return protectedValues{}, ErrUnavailable
	}
	return values, nil
}

func (s *Service) compile(root string, state State, exact *Binding) (Compiled, error) {
	if err := validateID(root); err != nil {
		return Compiled{}, err
	}
	managed := make(map[string]Revision, len(state.Records))
	for _, record := range state.Records {
		if !record.Enabled || record.Deleted {
			continue
		}
		revision := findRevision(state.Revisions, record.ID, record.CurrentRevisionID)
		if revision == nil {
			return Compiled{}, ErrUnavailable
		}
		managed[record.ID] = *revision
	}
	for id := range managed {
		if _, collision := s.configured[id]; collision {
			return Compiled{}, fmt.Errorf("%w: provider source collision", ErrInvalid)
		}
	}
	configs := make(map[string]agentconfig.Config)
	binding := Binding{RootProviderID: root, Reproducible: true}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string, int) error
	visit = func(id string, depth int) error {
		if visiting[id] {
			return fmt.Errorf("%w: provider graph contains a cycle", ErrInvalid)
		}
		if visited[id] {
			return nil
		}
		if depth > maxDepth || len(visited) >= maxProviders {
			return fmt.Errorf("%w: provider graph exceeds bounds", ErrInvalid)
		}
		visiting[id] = true
		var cfg agentconfig.Config
		var entry Entry
		if revision, ok := managed[id]; ok {
			if revision.FormatVersion != StorageFormatVersion {
				return ErrUnavailable
			}
			descriptor, found := s.registry.Lookup(revision.DriverType)
			if !found || descriptor.ContractVersion != revision.ContractVersion {
				return ErrUnavailable
			}
			values, err := s.open(revision)
			if err != nil {
				return err
			}
			publicDescriptor, found := s.registry.Lookup(revision.Definition.Config.Type)
			if !found || publicDescriptor.Type != revision.DriverType ||
				revision.Definition.HasAPIKey != (values.APIKey != "") ||
				!slicesEqual(revision.Definition.EnvironmentNames, sortedKeys(values.Environment)) {
				return ErrUnavailable
			}
			cfg = reconstruct(revision.Definition.Config, values)
			entry = Entry{ProviderID: id, Source: SourceManaged, RevisionID: revision.ID}
			if !revision.Definition.Reproducible {
				binding.Reproducible = false
				binding.Warnings = append(binding.Warnings, managedWarning(id, revision))
			}
		} else if configured, ok := s.configured[id]; ok {
			cfg = cloneConfig(configured)
			fingerprint, err := configuredFingerprint(cfg)
			if err != nil {
				return err
			}
			entry = Entry{ProviderID: id, Source: SourceConfigured, RevisionID: fingerprint}
			binding.Reproducible = false
			binding.Warnings = append(binding.Warnings, "provider "+id+" depends on configured external values")
		} else {
			return fmt.Errorf("%w: provider %q", ErrUnavailable, id)
		}
		if exact != nil {
			wanted := bindingEntry(*exact, id)
			if wanted == nil || *wanted != entry {
				return fmt.Errorf("%w: provider binding mismatch", ErrUnavailable)
			}
		}
		resolved, err := agentconfig.NormalizeConfig(cfg, "")
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		configs[id] = cfg
		binding.Entries = append(binding.Entries, entry)
		if resolved.DriverType == agentconfig.AgentTypePool {
			if len(resolved.PoolMembers) == 0 {
				return fmt.Errorf("%w: empty provider pool", ErrInvalid)
			}
			seen := make(map[string]bool, len(resolved.PoolMembers))
			for _, member := range resolved.PoolMembers {
				if seen[member] {
					return fmt.Errorf("%w: duplicate pool member", ErrInvalid)
				}
				seen[member] = true
				if err := visit(member, depth+1); err != nil {
					return err
				}
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	if err := visit(root, 0); err != nil {
		return Compiled{}, err
	}
	if exact != nil && len(binding.Entries) != len(exact.Entries) {
		return Compiled{}, fmt.Errorf("%w: provider binding contains unused entries", ErrUnavailable)
	}
	return Compiled{Binding: binding, Configs: configs}, nil
}

func (s *Service) validateCandidate(state State, record Record, revision Revision) error {
	next := State{Version: state.Version, Records: append([]Record(nil), state.Records...), Revisions: append([]Revision(nil), state.Revisions...)}
	replaced := false
	for i := range next.Records {
		if next.Records[i].ID == record.ID {
			next.Records[i] = record
			replaced = true
		}
	}
	if !replaced {
		next.Records = append(next.Records, record)
	}
	next.Revisions = append(next.Revisions, revision)
	for _, candidate := range next.Records {
		if candidate.Enabled && !candidate.Deleted {
			if _, err := s.compile(candidate.ID, next, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) validateLifecycle(state State, record Record) error {
	next := State{Version: state.Version, Records: append([]Record(nil), state.Records...), Revisions: state.Revisions}
	for i := range next.Records {
		if next.Records[i].ID == record.ID {
			next.Records[i] = record
		}
	}
	for _, candidate := range next.Records {
		if candidate.Enabled && !candidate.Deleted {
			if _, err := s.compile(candidate.ID, next, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func configuredFingerprint(cfg agentconfig.Config) (string, error) {
	public := cloneConfig(cfg)
	environmentNames := configEnvironmentNames(public)
	redact(&public)
	payload, err := json.Marshal(struct {
		Config           agentconfig.Config `json:"config"`
		EnvironmentNames []string           `json:"environment_names,omitempty"`
	}{Config: public, EnvironmentNames: environmentNames})
	if err != nil {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(payload)
	return "configured-v1:" + hex.EncodeToString(sum[:]), nil
}

func revisionAAD(providerID, revisionID string) []byte {
	payload, _ := json.Marshal([]string{"managed-provider-revision-v1", providerID, revisionID})
	return payload
}

func providerAudit(audit usercmd.AuditEvent, providerID string) usercmd.AuditEvent {
	audit.Action = usercmd.AuditActionProviderChanged
	audit.Outcome = usercmd.AuditOutcomeSucceeded
	audit.TargetType = usercmd.AuditTargetProvider
	audit.TargetID = providerID
	return audit
}

func rejectExplicitCommand(cfg agentconfig.Config) error {
	for _, block := range []*agentconfig.ACPConfig{cfg.GenericACP, cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil && len(block.Cmd) > 0 {
			return fmt.Errorf("%w: explicit ACP command is not managed", ErrInvalid)
		}
	}
	return nil
}

func containsPlaintext(cfg agentconfig.Config) bool {
	for _, block := range []*agentconfig.ACPConfig{cfg.GenericACP, cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil && len(block.Env) > 0 {
			return true
		}
	}
	for _, block := range []*agentconfig.LocalAPIConfig{cfg.OpenAI, cfg.AIStudio} {
		if block != nil && block.APIKey != "" {
			return true
		}
	}
	return false
}

func validateProtectedValues(class agentconfig.DriverClass, values protectedValues) error {
	switch class {
	case agentconfig.DriverClassACP:
		if values.APIKey != "" {
			return fmt.Errorf("%w: API key is unsupported for ACP providers", ErrInvalid)
		}
	case agentconfig.DriverClassHosted:
		if len(values.Environment) > 0 {
			return fmt.Errorf("%w: environment is unsupported for hosted providers", ErrInvalid)
		}
	case agentconfig.DriverClassPool:
		if values.APIKey != "" || len(values.Environment) > 0 {
			return fmt.Errorf("%w: protected values are unsupported for pools", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported provider class", ErrInvalid)
	}
	return nil
}

func managedReproducible(cfg agentconfig.Config, descriptor agentconfig.DriverDescriptor) bool {
	if descriptor.Type == agentconfig.AgentTypeRegistryACP {
		return false
	}
	if descriptor.Class != agentconfig.DriverClassACP {
		return true
	}
	for _, block := range []*agentconfig.ACPConfig{cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil {
			return block.InheritEnv != nil && !*block.InheritEnv
		}
	}
	return false
}

func managedWarning(id string, revision Revision) string {
	if revision.DriverType == agentconfig.AgentTypeRegistryACP {
		return "provider " + id + " uses a floating registry artifact"
	}
	return "provider " + id + " inherits external process environment"
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func configEnvironmentNames(cfg agentconfig.Config) []string {
	names := make(map[string]string)
	for _, block := range []*agentconfig.ACPConfig{cfg.GenericACP, cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil {
			for name := range block.Env {
				names[name] = ""
			}
		}
	}
	return sortedKeys(names)
}

func redact(cfg *agentconfig.Config) {
	for _, block := range []*agentconfig.ACPConfig{cfg.GenericACP, cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil {
			block.Env = nil
		}
	}
	for _, block := range []*agentconfig.LocalAPIConfig{cfg.OpenAI, cfg.AIStudio} {
		if block != nil {
			block.APIKey = ""
		}
	}
}

func reconstruct(public agentconfig.Config, values protectedValues) agentconfig.Config {
	cfg := cloneConfig(public)
	for _, block := range []*agentconfig.ACPConfig{cfg.GenericACP, cfg.GeminiACP, cfg.CodexACP, cfg.OpenCodeACP, cfg.CopilotACP, cfg.ClaudeCodeACP, cfg.GrokACP, cfg.RegistryACP, cfg.AgyACP, cfg.AntigravityACP} {
		if block != nil && len(values.Environment) > 0 {
			block.Env = cloneStrings(values.Environment)
		}
	}
	if cfg.OpenAI != nil {
		cfg.OpenAI.APIKey = values.APIKey
	}
	if cfg.AIStudio != nil {
		cfg.AIStudio.APIKey = values.APIKey
	}
	return cfg
}

func immutableIdentity(revision Revision) string {
	registryID := ""
	if revision.Definition.Config.RegistryACP != nil {
		registryID = revision.Definition.Config.RegistryACP.RegistryID
	}
	return revision.DriverType + "\x00" + registryID
}

func validateID(id string) error {
	if strings.TrimSpace(id) != id || id == "" || len(id) > maxIDLength {
		return fmt.Errorf("%w: invalid provider id", ErrInvalid)
	}
	return nil
}

func findRecord(records []Record, id string) *Record {
	for i := range records {
		if records[i].ID == id {
			return &records[i]
		}
	}
	return nil
}

func recordRevisionID(record *Record) string {
	if record == nil {
		return ""
	}
	return record.CurrentRevisionID
}

func findRevision(revisions []Revision, providerID, revisionID string) *Revision {
	for i := range revisions {
		if revisions[i].ProviderID == providerID && revisions[i].ID == revisionID {
			return &revisions[i]
		}
	}
	return nil
}

func bindingEntry(binding Binding, id string) *Entry {
	for i := range binding.Entries {
		if binding.Entries[i].ProviderID == id {
			return &binding.Entries[i]
		}
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneConfigs(configs map[string]agentconfig.Config) map[string]agentconfig.Config {
	cloned := make(map[string]agentconfig.Config, len(configs))
	for id, cfg := range configs {
		cloned[id] = cloneConfig(cfg)
	}
	return cloned
}

func cloneConfig(cfg agentconfig.Config) agentconfig.Config {
	payload, _ := json.Marshal(cfg)
	var cloned agentconfig.Config
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
