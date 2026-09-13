package runtimecatalog

import (
	"fmt"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// Store retains immutable snapshots and the active application snapshot.
type Store struct {
	mu          sync.RWMutex
	sequence    uint64
	snapshots   map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot
	application runtimecatalogcmd.SnapshotID
}

// NewStore creates an empty retained snapshot store.
func NewStore() *Store {
	return &Store{snapshots: make(map[runtimecatalogcmd.SnapshotID]runtimecatalogcmd.Snapshot)}
}

// Publish retains a snapshot and assigns its process-local sequence.
func (s *Store) Publish(snapshot runtimecatalogcmd.Snapshot) (runtimecatalogcmd.Snapshot, error) {
	if s == nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("runtime catalog store is required")
	}
	if err := verifySnapshotID(snapshot); err != nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("verify snapshot ID: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	retained := s.publishLocked(snapshot)
	return retained.Clone(), nil
}

// PublishApplication retains and activates an application snapshot.
func (s *Store) PublishApplication(snapshot runtimecatalogcmd.Snapshot) (runtimecatalogcmd.Snapshot, error) {
	if snapshot.Scope.Kind != runtimecatalogcmd.SnapshotScopeApplication {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("application snapshot is required")
	}
	if s == nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("runtime catalog store is required")
	}
	if err := verifySnapshotID(snapshot); err != nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("verify snapshot ID: %w", err)
	}
	s.mu.Lock()
	retained := s.publishLocked(snapshot)
	s.application = retained.ID
	s.mu.Unlock()
	return retained.Clone(), nil
}

func (s *Store) publishLocked(snapshot runtimecatalogcmd.Snapshot) runtimecatalogcmd.Snapshot {
	if retained, ok := s.snapshots[snapshot.ID]; ok {
		return retained
	}
	s.sequence++
	retained := snapshot.Clone()
	retained.Sequence = s.sequence
	s.snapshots[retained.ID] = retained
	return retained
}

// Get returns a copy of a retained snapshot.
func (s *Store) Get(id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	if s == nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("%w: %s", ErrSnapshotUnavailable, id)
	}
	s.mu.RLock()
	snapshot, ok := s.snapshots[id]
	s.mu.RUnlock()
	if !ok {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("%w: %s", ErrSnapshotUnavailable, id)
	}
	return snapshot.Clone(), nil
}

// Application returns the active application snapshot.
func (s *Store) Application() (runtimecatalogcmd.Snapshot, error) {
	if s == nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("%w: application", ErrSnapshotUnavailable)
	}
	s.mu.RLock()
	id := s.application
	s.mu.RUnlock()
	if id == "" {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("%w: application", ErrSnapshotUnavailable)
	}
	return s.Get(id)
}
