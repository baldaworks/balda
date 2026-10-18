package skillapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/google/uuid"
)

// Catalog owns immutable capture, complete compilation and publication. All
// accepting operations hold its shared gate through guarded SQL and publication.
type Catalog interface {
	LockCatalogMutation(ctx context.Context) (func(), error)
	CaptureCandidate(ctx context.Context, root *os.Root, relative string, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, error)
	PrepareSkillCandidate(ctx context.Context, selection runtimecatalogcmd.ManagedSkillSelection) (runtimecatalogcmd.PreparedApplicationCandidate, error)
	PublishCandidate(ctx context.Context, candidate runtimecatalogcmd.PreparedApplicationCandidate) error
	RetrySkillPublication(ctx context.Context) error
	CurrentSkillSnapshot(ctx context.Context, workspace string) (runtimecatalogcmd.Snapshot, error)
}

// InstallRequest contains transient explicit source input; never persist or echo
// it as browser output. Path selects one skill, defaulting to the source root.
type InstallRequest struct {
	Kind          string
	Source        string
	Ref           string
	Path          string
	ExpectedState string
	Authority     skillcmd.Authority
}

type reviewed struct {
	candidate    skillcmd.Candidate
	installation skillcmd.Installation
	authority    skillcmd.Authority
	operation    skillcmd.Operation
}

// Service owns standalone origin policy and administrator lifecycle admission.
type Service struct {
	stagingDir string
	store      ManagementStore
	catalog    Catalog
	mu         sync.Mutex
	candidates map[string]reviewed
	diagnostic string
	now        func() time.Time
	readOnly   bool
}

// NewReadOnly constructs inventory-only management without creating or
// cleaning staging paths. Callers must not use mutation methods on this value.
func NewReadOnly(store ManagementStore, catalog Catalog) (*Service, error) {
	if store == nil || catalog == nil {
		return nil, skillcmd.ErrUnavailable
	}
	return &Service{store: store, catalog: catalog, candidates: make(map[string]reviewed), now: time.Now, readOnly: true}, nil
}

// New constructs management without reading or fetching any source.
func New(stateDir string, store ManagementStore, catalog Catalog) (*Service, error) {
	if store == nil || catalog == nil {
		return nil, skillcmd.ErrUnavailable
	}
	if strings.TrimSpace(stateDir) == "" {
		return nil, skillcmd.ErrInvalid
	}
	staging := filepath.Join(stateDir, "skill-staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return nil, skillcmd.ErrUnavailable
	}
	info, err := os.Lstat(staging)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, skillcmd.ErrUnavailable
	}
	if err := os.Chmod(staging, 0700); err != nil {
		return nil, skillcmd.ErrUnavailable
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return nil, skillcmd.ErrUnavailable
	}
	// One process owns a state directory. Only our UUID-named transient clones
	// are reclaimed here; source directories and revision archives are untouched.
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "source-") {
			continue
		}
		if _, err := uuid.Parse(strings.TrimPrefix(entry.Name(), "source-")); err != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(staging, entry.Name())); err != nil {
			return nil, skillcmd.ErrUnavailable
		}
	}
	return &Service{stagingDir: staging, store: store, catalog: catalog, candidates: make(map[string]reviewed), now: time.Now}, nil
}

func safeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	for _, target := range []error{skillcmd.ErrInvalid, skillcmd.ErrForbidden, skillcmd.ErrConflict, skillcmd.ErrNotFound} {
		if errors.Is(err, target) {
			return target
		}
	}
	return skillcmd.ErrUnavailable
}

func (s *Service) observe(ctx context.Context, authority skillcmd.Authority) (skillcmd.Observation, error) {
	if !authority.Valid() {
		return skillcmd.Observation{}, skillcmd.ErrForbidden
	}
	if err := s.store.CheckAuthority(ctx, authority); err != nil {
		return skillcmd.Observation{}, safeError(err)
	}
	observation, err := s.store.Observe(ctx)
	return observation, safeError(err)
}

// Inventory projects retained metadata and desired state without source access.
func (s *Service) Inventory(ctx context.Context, authority skillcmd.Authority) (skillcmd.Inventory, error) {
	observation, err := s.observe(ctx, authority)
	if err != nil {
		return skillcmd.Inventory{}, err
	}
	snapshot, err := s.catalog.CurrentSkillSnapshot(ctx, "")
	if err != nil {
		return skillcmd.Inventory{}, safeError(err)
	}
	result := skillcmd.Inventory{ExpectedState: observation.Token(), Generation: observation.Record.Generation, Pending: observation.Record.Pending}
	installations := make(map[runtimecatalogcmd.SourceID]skillcmd.Installation)
	for _, installation := range observation.Record.Installations {
		installations[installation.Descriptor.ID] = installation
	}
	for _, metadata := range snapshot.Skills {
		if _, managed := installations[metadata.ID.Source]; managed {
			continue
		}
		entry := skillcmd.Entry{ID: metadata.ID, Revision: metadata.Revision, CurrentRevision: metadata.Revision, Name: metadata.Name, Description: metadata.Description, Enabled: true, Ownership: skillcmd.OwnershipUnmanaged, SourceLabel: sourceLabel(metadata.ID.Source)}
		if metadata.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			entry.Ownership = skillcmd.OwnershipPlugin
			entry.PluginID = metadata.ID.Source.Name
		}
		result.Entries = append(result.Entries, entry)
	}
	for _, installation := range observation.Record.Installations {
		if installation.Removed {
			continue
		}
		entry := installationEntry(installation)
		entry.Pending = observation.Record.Pending
		if current, ok := snapshot.Sources[installation.Descriptor.ID]; ok {
			entry.CurrentRevision = current.Revision
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].ID.String() < result.Entries[j].ID.String() })
	for _, diagnostic := range snapshot.Diagnostics {
		result.Diagnostics = append(result.Diagnostics, diagnostic.Code)
	}
	s.mu.Lock()
	if s.diagnostic != "" {
		result.Diagnostics = append(result.Diagnostics, s.diagnostic)
	}
	s.mu.Unlock()
	return result, nil
}

func sourceLabel(id runtimecatalogcmd.SourceID) string {
	if id.Kind == runtimecatalogcmd.SourceKindPlugin {
		return "Plugin: " + id.Name
	}
	switch id.Name {
	case "agents-global":
		return "Global .agents/skills"
	case "codex-global":
		return "Global Codex skills"
	case "default":
		return "Balda skills directory"
	default:
		return string(id.Kind) + ": " + id.Name
	}
}

func installationEntry(i skillcmd.Installation) skillcmd.Entry {
	return skillcmd.Entry{ID: runtimecatalogcmd.ContributionID{Source: i.Descriptor.ID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: i.Name}, Revision: i.Descriptor.Revision, Name: i.Name, Description: i.Description, Ownership: skillcmd.OwnershipManaged, SourceLabel: originLabel(i.Origin), Enabled: i.Enabled}
}

// ReviewInstall captures one independent subtree without changing selection.
func (s *Service) ReviewInstall(ctx context.Context, request InstallRequest) (skillcmd.Candidate, error) {
	if s.readOnly {
		return skillcmd.Candidate{}, skillcmd.ErrForbidden
	}
	origin, err := normalizeOrigin(request)
	if err != nil {
		return skillcmd.Candidate{}, err
	}
	observation, err := s.observe(ctx, request.Authority)
	if err != nil {
		return skillcmd.Candidate{}, err
	}
	if origin.Kind == sourceLocal {
		canonical, err := filepath.EvalSymlinks(origin.Source)
		if err != nil {
			return skillcmd.Candidate{}, skillcmd.ErrUnavailable
		}
		origin.Source = canonical
	}
	if observation.Token() != request.ExpectedState || observation.Record.Pending {
		return skillcmd.Candidate{}, skillcmd.ErrConflict
	}
	for _, i := range observation.Record.Installations {
		if !i.Removed && sameOrigin(i.Origin, origin) {
			return skillcmd.Candidate{}, skillcmd.ErrConflict
		}
	}
	installation := skillcmd.Installation{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-" + uuid.NewString()}}, Origin: origin, Enabled: true}
	return s.review(ctx, observation, installation, request.Authority, skillcmd.OperationInstall)
}

// ReviewUpdate uses only the accepted private origin, preserving enablement.
func (s *Service) ReviewUpdate(ctx context.Context, mutation skillcmd.Mutation) (skillcmd.Candidate, error) {
	if s.readOnly {
		return skillcmd.Candidate{}, skillcmd.ErrForbidden
	}
	observation, err := s.observe(ctx, mutation.Authority)
	if err != nil {
		return skillcmd.Candidate{}, err
	}
	installation, err := findInstallation(observation, mutation)
	if err != nil {
		return skillcmd.Candidate{}, err
	}
	return s.review(ctx, observation, installation, mutation.Authority, skillcmd.OperationUpdate)
}

func (s *Service) review(ctx context.Context, observation skillcmd.Observation, installation skillcmd.Installation, authority skillcmd.Authority, operation skillcmd.Operation) (skillcmd.Candidate, error) {
	source, commit, err := s.capture(ctx, installation.Origin, installation.Descriptor.ID)
	if err != nil {
		return skillcmd.Candidate{}, safeError(err)
	}
	if len(source.Skills) != 1 || len(source.Diagnostics) != 0 {
		return skillcmd.Candidate{}, skillcmd.ErrInvalid
	}
	metadata := source.Skills[0]
	if operation == skillcmd.OperationUpdate && metadata.Name != installation.Name {
		return skillcmd.Candidate{}, skillcmd.ErrConflict
	}
	installation.Descriptor = source.Descriptor
	installation.Name = metadata.Name
	installation.Description = metadata.Description
	installation.ResolvedCommit = commit
	candidate := skillcmd.Candidate{Handle: uuid.NewString(), Entry: installationEntry(installation), SkillPath: installation.Origin.Path, ResolvedCommit: commit, ExpectedState: observation.Token(), ExpiresAt: s.now().Add(10 * time.Minute)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for handle, item := range s.candidates {
		if !s.now().Before(item.candidate.ExpiresAt) {
			delete(s.candidates, handle)
		}
	}
	if len(s.candidates) >= 128 {
		return skillcmd.Candidate{}, skillcmd.ErrUnavailable
	}
	s.candidates[candidate.Handle] = reviewed{candidate: candidate, installation: installation, authority: authority, operation: operation}
	return candidate, nil
}

func sameAuthority(a, b skillcmd.Authority) bool {
	a.At = time.Time{}
	b.At = time.Time{}
	return a == b
}

// Accept rechecks authority/state and rematerializes exact reviewed bytes under
// the catalog gate. No browser input can exchange the reviewed origin or path.
func (s *Service) Accept(ctx context.Context, handle string, authority skillcmd.Authority) (skillcmd.Outcome, error) {
	if s.readOnly {
		return skillcmd.Outcome{}, skillcmd.ErrForbidden
	}
	s.mu.Lock()
	item, found := s.candidates[handle]
	s.mu.Unlock()
	if !found || !s.now().Before(item.candidate.ExpiresAt) {
		return skillcmd.Outcome{}, skillcmd.ErrConflict
	}
	if !sameAuthority(item.authority, authority) {
		return skillcmd.Outcome{}, skillcmd.ErrForbidden
	}
	unlock, err := s.catalog.LockCatalogMutation(ctx)
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	defer unlock()
	observation, err := s.observe(ctx, authority)
	if err != nil {
		return skillcmd.Outcome{}, err
	}
	if observation.Token() != item.candidate.ExpectedState || observation.Record.Pending || !s.now().Before(item.candidate.ExpiresAt) {
		return skillcmd.Outcome{}, skillcmd.ErrConflict
	}
	source, commit, err := s.capture(ctx, item.installation.Origin, item.installation.Descriptor.ID)
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	if source.Descriptor != item.installation.Descriptor || commit != item.installation.ResolvedCommit || len(source.Skills) != 1 || source.Skills[0].Name != item.installation.Name {
		return skillcmd.Outcome{}, skillcmd.ErrConflict
	}
	record := observation.Record
	record.Installations = slices.Clone(record.Installations)
	now := s.now()
	installation := item.installation
	installation.UpdatedAt = now
	if item.operation == skillcmd.OperationInstall {
		for _, i := range record.Installations {
			if !i.Removed && sameOrigin(i.Origin, installation.Origin) {
				return skillcmd.Outcome{}, skillcmd.ErrConflict
			}
		}
		installation.CreatedAt = now
		record.Installations = append(record.Installations, installation)
	} else {
		index := installationIndex(record, installation.Descriptor.ID)
		if index < 0 || record.Installations[index].Removed || record.Installations[index].Origin != installation.Origin || record.Installations[index].Name != installation.Name {
			return skillcmd.Outcome{}, skillcmd.ErrConflict
		}
		installation.Enabled = record.Installations[index].Enabled
		record.Installations[index] = installation
	}
	outcome, err := s.acceptRecord(ctx, observation, record, authority, item.operation, installation.Descriptor.ID.Name)
	if outcome.Accepted {
		entry := installationEntry(installation)
		entry.Pending = outcome.Pending
		outcome.Entry = &entry
		s.mu.Lock()
		delete(s.candidates, handle)
		s.mu.Unlock()
	}
	return outcome, err
}

func installationIndex(record skillcmd.Record, id runtimecatalogcmd.SourceID) int {
	for index, i := range record.Installations {
		if i.Descriptor.ID == id {
			return index
		}
	}
	return -1
}

func findInstallation(observation skillcmd.Observation, mutation skillcmd.Mutation) (skillcmd.Installation, error) {
	if mutation.ID.Kind != runtimecatalogcmd.SourceKindUserSkill || !strings.HasPrefix(mutation.ID.Name, "managed-") {
		return skillcmd.Installation{}, skillcmd.ErrForbidden
	}
	if observation.Token() != mutation.ExpectedState || observation.Record.Pending {
		return skillcmd.Installation{}, skillcmd.ErrConflict
	}
	index := installationIndex(observation.Record, mutation.ID)
	if index < 0 || observation.Record.Installations[index].Removed {
		return skillcmd.Installation{}, skillcmd.ErrNotFound
	}
	return observation.Record.Installations[index], nil
}

// Change enables, disables or removes only a managed installation; it never
// fetches an origin or deletes retained payloads, user skills, or plugin files.
func (s *Service) Change(ctx context.Context, operation skillcmd.Operation, mutation skillcmd.Mutation) (skillcmd.Outcome, error) {
	if s.readOnly {
		return skillcmd.Outcome{}, skillcmd.ErrForbidden
	}
	if operation != skillcmd.OperationEnable && operation != skillcmd.OperationDisable && operation != skillcmd.OperationRemove {
		return skillcmd.Outcome{}, skillcmd.ErrInvalid
	}
	unlock, err := s.catalog.LockCatalogMutation(ctx)
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	defer unlock()
	observation, err := s.observe(ctx, mutation.Authority)
	if err != nil {
		return skillcmd.Outcome{}, err
	}
	installation, err := findInstallation(observation, mutation)
	if err != nil {
		return skillcmd.Outcome{}, err
	}
	installation.UpdatedAt = s.now()
	switch operation {
	case skillcmd.OperationEnable:
		installation.Enabled = true
	case skillcmd.OperationDisable:
		installation.Enabled = false
	case skillcmd.OperationRemove:
		installation.Removed = true
		installation.Enabled = false
	}
	record := observation.Record
	record.Installations = slices.Clone(record.Installations)
	record.Installations[installationIndex(record, mutation.ID)] = installation
	return s.acceptRecord(ctx, observation, record, mutation.Authority, operation, mutation.ID.Name)
}

// Refresh explicitly rebuilds eligible global filesystem contributions.
func (s *Service) Refresh(ctx context.Context, mutation skillcmd.Mutation) (skillcmd.Outcome, error) {
	if s.readOnly {
		return skillcmd.Outcome{}, skillcmd.ErrForbidden
	}
	unlock, err := s.catalog.LockCatalogMutation(ctx)
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	defer unlock()
	observation, err := s.observe(ctx, mutation.Authority)
	if err != nil {
		return skillcmd.Outcome{}, err
	}
	if observation.Token() != mutation.ExpectedState || observation.Record.Pending {
		return skillcmd.Outcome{}, skillcmd.ErrConflict
	}
	outcome, err := s.acceptRecord(ctx, observation, observation.Record, mutation.Authority, skillcmd.OperationRefresh, "")
	s.mu.Lock()
	s.diagnostic = ""
	if err != nil {
		s.diagnostic = "refresh-unavailable"
	}
	s.mu.Unlock()
	return outcome, err
}

func (s *Service) acceptRecord(ctx context.Context, observation skillcmd.Observation, record skillcmd.Record, authority skillcmd.Authority, operation skillcmd.Operation, target string) (skillcmd.Outcome, error) {
	record.SchemaVersion = 1
	record.Generation++
	record.Pending = true
	record.UpdatedAt = s.now()
	if record.Validate() != nil {
		return skillcmd.Outcome{}, skillcmd.ErrInvalid
	}
	prepared, err := s.catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	err = s.store.Commit(ctx, skillcmd.Commit{Authority: authority, ExpectedState: observation.Token(), Record: record, Operation: operation, TargetID: target, AuditID: uuid.NewString()})
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	outcome := skillcmd.Outcome{Accepted: true, Pending: true, Generation: record.Generation}
	// Durable acceptance is final even when projection or completion fails.
	if err := s.catalog.PublishCandidate(ctx, prepared); err == nil {
		outcome.Pending = false
	}
	return outcome, nil
}

// RetryPublication rebuilds current desired state and creates no second audit.
func (s *Service) RetryPublication(ctx context.Context, authority skillcmd.Authority) (skillcmd.Outcome, error) {
	if s.readOnly {
		return skillcmd.Outcome{}, skillcmd.ErrForbidden
	}
	unlock, err := s.catalog.LockCatalogMutation(ctx)
	if err != nil {
		return skillcmd.Outcome{}, safeError(err)
	}
	defer unlock()
	observation, err := s.observe(ctx, authority)
	if err != nil {
		return skillcmd.Outcome{}, err
	}
	outcome := skillcmd.Outcome{Accepted: true, Pending: observation.Record.Pending, Generation: observation.Record.Generation}
	if !outcome.Pending {
		return outcome, nil
	}
	if err := s.catalog.RetrySkillPublication(ctx); err != nil {
		return outcome, safeError(err)
	}
	outcome.Pending = false
	return outcome, nil
}
