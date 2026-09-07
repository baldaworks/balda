package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const destinationKVKey = "destinations"

type destinationKVStore interface {
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	SetJSON(ctx context.Context, key string, value any) error
}

// DestinationStore manages persistent, transport-neutral delivery destination records.
type DestinationStore struct {
	store destinationKVStore
	mu    sync.RWMutex
	items map[string]deliverycmd.DestinationRecord
}

// NewDestinationStore creates a new destination store backed by key-value storage.
func NewDestinationStore(stateStore destinationKVStore) (*DestinationStore, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("destination state store is required")
	}
	s := &DestinationStore{
		store: stateStore,
		items: make(map[string]deliverycmd.DestinationRecord),
	}
	if err := s.load(); err != nil {
		return nil, fmt.Errorf("loading destinations: %w", err)
	}
	return s, nil
}

// RegisterDestination registers or updates a delivery destination.
func (s *DestinationStore) RegisterDestination(_ context.Context, dest deliverycmd.DestinationRecord) error {
	if err := dest.Validate(); err != nil {
		return fmt.Errorf("invalid destination record: %w", err)
	}

	dest.ChannelType = strings.ToLower(strings.TrimSpace(dest.ChannelType))
	dest.Roles = deliverycmd.NormalizeRoles(dest.Roles)
	locatorRef := dest.LocatorRef()
	if locatorRef == "" {
		return fmt.Errorf("destination locator ref is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	dest.UpdatedAt = now
	if existing, exists := s.items[locatorRef]; exists && !existing.CreatedAt.IsZero() {
		dest.CreatedAt = existing.CreatedAt
	} else if dest.CreatedAt.IsZero() {
		dest.CreatedAt = now
	}

	// If this destination is marked as default, clear default on any other destination sharing its roles.
	if dest.IsDefault {
		for ref, other := range s.items {
			if ref == locatorRef {
				continue
			}
			for _, r := range dest.Roles {
				if other.HasRole(r) && other.IsDefault {
					other.IsDefault = false
					other.UpdatedAt = now
					s.items[ref] = other
					break
				}
			}
		}
	}

	s.items[locatorRef] = dest
	return s.saveLocked()
}

// GetDestination retrieves a destination by its canonical locator ref ("<channel_type>:<address_key>").
func (s *DestinationStore) GetDestination(_ context.Context, locatorRef string) (deliverycmd.DestinationRecord, bool, error) {
	trimmed := strings.TrimSpace(locatorRef)
	if trimmed == "" {
		return deliverycmd.DestinationRecord{}, false, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	dest, ok := s.items[trimmed]
	if !ok {
		return deliverycmd.DestinationRecord{}, false, nil
	}
	return cloneDestination(dest), true, nil
}

// ListDestinations returns all registered destinations sorted deterministically by locator ref.
func (s *DestinationStore) ListDestinations(_ context.Context) ([]deliverycmd.DestinationRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]deliverycmd.DestinationRecord, 0, len(s.items))
	for _, dest := range s.items {
		out = append(out, cloneDestination(dest))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LocatorRef() < out[j].LocatorRef()
	})
	return out, nil
}

// GetDestinationsByRole returns all destinations assigned the given role, sorted deterministically.
func (s *DestinationStore) GetDestinationsByRole(_ context.Context, role string) ([]deliverycmd.DestinationRecord, error) {
	trimmed := strings.ToLower(strings.TrimSpace(role))
	if trimmed == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]deliverycmd.DestinationRecord, 0)
	for _, dest := range s.items {
		if dest.HasRole(trimmed) {
			out = append(out, cloneDestination(dest))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LocatorRef() < out[j].LocatorRef()
	})
	return out, nil
}

// GetDefaultDestinationByRole returns the default destination for a given role, if configured.
func (s *DestinationStore) GetDefaultDestinationByRole(_ context.Context, role string) (deliverycmd.DestinationRecord, bool, error) {
	trimmed := strings.ToLower(strings.TrimSpace(role))
	if trimmed == "" {
		return deliverycmd.DestinationRecord{}, false, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, dest := range s.items {
		if dest.HasRole(trimmed) && dest.IsDefault {
			return cloneDestination(dest), true, nil
		}
	}
	return deliverycmd.DestinationRecord{}, false, nil
}

// SetDefaultDestination marks the destination for locatorRef as default for the specified role.
func (s *DestinationStore) SetDefaultDestination(_ context.Context, locatorRef string, role string) error {
	trimmedRef := strings.TrimSpace(locatorRef)
	if trimmedRef == "" {
		return fmt.Errorf("destination locator ref is required")
	}
	trimmedRole := strings.ToLower(strings.TrimSpace(role))
	if trimmedRole == "" {
		return fmt.Errorf("role is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	target, exists := s.items[trimmedRef]
	if !exists {
		return fmt.Errorf("destination %q not found", trimmedRef)
	}

	now := time.Now().UTC()
	if !target.HasRole(trimmedRole) {
		target.Roles = append(target.Roles, trimmedRole)
	}
	target.IsDefault = true
	target.UpdatedAt = now

	// Clear default on any other destination with this role.
	for ref, other := range s.items {
		if ref == trimmedRef {
			continue
		}
		if other.HasRole(trimmedRole) && other.IsDefault {
			other.IsDefault = false
			other.UpdatedAt = now
			s.items[ref] = other
		}
	}

	s.items[trimmedRef] = target
	return s.saveLocked()
}

// RemoveDestination removes a destination by its canonical locator ref.
func (s *DestinationStore) RemoveDestination(_ context.Context, locatorRef string) error {
	trimmed := strings.TrimSpace(locatorRef)
	if trimmed == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.items, trimmed)
	return s.saveLocked()
}

func (s *DestinationStore) load() error {
	raw, ok, err := s.store.GetJSON(context.Background(), destinationKVKey)
	if err != nil {
		return fmt.Errorf("get destinations state: %w", err)
	}
	if !ok || raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal destinations state: %w", err)
	}

	var stored []deliverycmd.DestinationRecord
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("unmarshal destinations: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]deliverycmd.DestinationRecord, len(stored))
	for _, dest := range stored {
		dest.Roles = deliverycmd.NormalizeRoles(dest.Roles)
		dest.ChannelType = strings.ToLower(strings.TrimSpace(dest.ChannelType))
		ref := dest.LocatorRef()
		if ref != "" {
			s.items[ref] = dest
		}
	}
	return nil
}

func (s *DestinationStore) saveLocked() error {
	list := make([]deliverycmd.DestinationRecord, 0, len(s.items))
	for _, dest := range s.items {
		list = append(list, dest)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].LocatorRef() < list[j].LocatorRef()
	})
	if err := s.store.SetJSON(context.Background(), destinationKVKey, list); err != nil {
		return fmt.Errorf("set destinations state: %w", err)
	}
	return nil
}

func cloneDestination(in deliverycmd.DestinationRecord) deliverycmd.DestinationRecord {
	out := in
	if len(in.Roles) > 0 {
		out.Roles = append([]string(nil), in.Roles...)
	}
	return out
}
