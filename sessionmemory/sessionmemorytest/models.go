package sessionmemorytest

import (
	"context"
	"sync"

	"github.com/normahq/balda/sessionmemory"
)

// ModelCalls reports deterministic fake-model invocation counts.
type ModelCalls struct {
	Atoms     int
	Scenarios int
	Profile   int
}

// Models is a thread-safe scripted implementation of all three model ports.
// Candidates are copied at both configuration and call boundaries.
type Models struct {
	mu sync.Mutex

	atoms     []sessionmemory.AtomCandidate
	scenarios []sessionmemory.ScenarioCandidate
	profile   *sessionmemory.ProfileCandidate

	atomErr     error
	scenarioErr error
	profileErr  error
	calls       ModelCalls
}

// NewModels returns an empty scripted model fixture.
func NewModels() *Models {
	return &Models{}
}

// SetAtoms replaces the atom extraction script and optional failure.
func (m *Models) SetAtoms(candidates []sessionmemory.AtomCandidate, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.atoms, _ = clone(candidates)
	m.atomErr = err
}

// SetScenarios replaces the scenario synthesis script and optional failure.
func (m *Models) SetScenarios(candidates []sessionmemory.ScenarioCandidate, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenarios, _ = clone(candidates)
	m.scenarioErr = err
}

// SetProfile replaces the profile synthesis script and optional failure.
func (m *Models) SetProfile(candidate *sessionmemory.ProfileCandidate, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profile, _ = clone(candidate)
	m.profileErr = err
}

// Calls returns a consistent invocation-count snapshot.
func (m *Models) Calls() ModelCalls {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// ExtractAtoms implements sessionmemory.AtomExtractor.
func (m *Models) ExtractAtoms(
	ctx context.Context,
	_ sessionmemory.AtomExtractionRequest,
) ([]sessionmemory.AtomCandidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.Atoms++
	if m.atomErr != nil {
		return nil, m.atomErr
	}
	return clone(m.atoms)
}

// SynthesizeScenarios implements sessionmemory.ScenarioSynthesizer.
func (m *Models) SynthesizeScenarios(
	ctx context.Context,
	_ sessionmemory.ScenarioSynthesisRequest,
) ([]sessionmemory.ScenarioCandidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.Scenarios++
	if m.scenarioErr != nil {
		return nil, m.scenarioErr
	}
	return clone(m.scenarios)
}

// SynthesizeProfile implements sessionmemory.ProfileSynthesizer.
func (m *Models) SynthesizeProfile(
	ctx context.Context,
	_ sessionmemory.ProfileSynthesisRequest,
) (*sessionmemory.ProfileCandidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls.Profile++
	if m.profileErr != nil {
		return nil, m.profileErr
	}
	return clone(m.profile)
}
