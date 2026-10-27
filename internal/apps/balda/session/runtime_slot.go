package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"

	"github.com/baldaworks/runtime/v2/agentproduct"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

var (
	// ErrRuntimeUnavailable reports that a slot no longer accepts leases.
	ErrRuntimeUnavailable = errors.New("session runtime is unavailable")
	// ErrRuntimeCleanupIncomplete reports that cleanup remains pending without
	// exposing provider details or closing resources still in use.
	ErrRuntimeCleanupIncomplete = errors.New("session runtime cleanup incomplete")
	// ErrRuntimeCloseFailed is the bounded classification for aggregate runtime
	// cleanup failures. Provider-specific details do not cross this boundary.
	ErrRuntimeCloseFailed = errors.New("session runtime close failed")
	// ErrRuntimeRecoveryRequired reports that durable and in-memory runtime
	// authority may disagree and admission must remain fenced.
	ErrRuntimeRecoveryRequired = errors.New("session runtime recovery required")
	// ErrRuntimePublicationConflict reports a definitive stale publication guard.
	ErrRuntimePublicationConflict = errors.New("session runtime publication conflict")
	// ErrRuntimePublicationInvalid reports a malformed publication request.
	ErrRuntimePublicationInvalid = errors.New("invalid session runtime publication")
)

// RuntimeLease is an immutable view of one coherent session runtime. Release
// must be called when the turn no longer uses the runtime.
type RuntimeLease struct {
	slot *RuntimeSlot
	once sync.Once
}

// Agent returns the agent bound to the leased runtime.
func (l *RuntimeLease) Agent() agent.Agent { return l.slot.value.runtime.Agent }

// Runner returns the runner bound to the leased runtime.
func (l *RuntimeLease) Runner() *runner.Runner { return l.slot.value.runtime.Runner }

// SessionControl returns the optional control plane adjacent to the agent.
func (l *RuntimeLease) SessionControl() agentproduct.SessionControl {
	return l.slot.value.runtime.SessionControl
}

// SessionService returns the exact ADK session service bound to the runtime.
func (l *RuntimeLease) SessionService() adksession.Service {
	return l.slot.value.runtime.SessionSvc
}

// Session returns the exact ADK session bound to the runtime.
func (l *RuntimeLease) Session() adksession.Session { return l.slot.value.session }

// AppName returns the exact ADK application identity.
func (l *RuntimeLease) AppName() string { return l.slot.value.appName }

// UserID returns the exact ADK user identity.
func (l *RuntimeLease) UserID() string { return l.slot.value.userID }

// SessionID returns the exact ADK session identity.
func (l *RuntimeLease) SessionID() string { return l.slot.value.sessionID }

// ArchiveID returns the conversation lifetime fence carried by the lease.
func (l *RuntimeLease) ArchiveID() string { return l.slot.value.archiveID }

// Generation returns the committed runtime generation carried by the lease.
func (l *RuntimeLease) Generation() uint64 { return l.slot.value.generation }

// EffectiveModel returns the public model metadata carried by the lease.
func (l *RuntimeLease) EffectiveModel() string { return l.slot.value.effectiveModel }

// Outcome returns the bounded publication outcome carried by the lease.
func (l *RuntimeLease) Outcome() string { return l.slot.value.outcome }

// RuntimeSnapshotID returns the immutable capability snapshot bound to the runtime.
func (l *RuntimeLease) RuntimeSnapshotID() string {
	return l.slot.value.runtime.RuntimeSnapshotID
}

// ProviderBindingJSON returns the canonical public provider binding.
func (l *RuntimeLease) ProviderBindingJSON() string {
	return l.slot.value.runtime.ProviderBindingJSON
}

// RuntimeStateValue reads one state value through the exact ADK identity held
// by this lease. It never consults the session's current runtime.
func (l *RuntimeLease) RuntimeStateValue(ctx context.Context, key string) (any, bool, error) {
	if l == nil || l.slot == nil || l.Session() == nil || l.SessionService() == nil {
		return nil, false, nil
	}
	current, err := l.SessionService().Get(ctx, &adksession.GetRequest{
		AppName:   l.AppName(),
		UserID:    l.UserID(),
		SessionID: l.SessionID(),
	})
	if err != nil {
		return nil, false, fmt.Errorf("get runtime session: %w", err)
	}
	if current == nil || current.Session == nil || current.Session.State() == nil {
		return nil, false, nil
	}
	value, err := current.Session.State().Get(strings.TrimSpace(key))
	if err != nil {
		return nil, false, nil
	}
	return value, true, nil
}

// Release gives up the lease exactly once.
func (l *RuntimeLease) Release() {
	if l == nil || l.slot == nil {
		return
	}
	l.once.Do(l.slot.releaseLease)
}

type runtimeSlotValue struct {
	runtime        BuiltRuntime
	session        adksession.Session
	appName        string
	userID         string
	sessionID      string
	archiveID      string
	generation     uint64
	effectiveModel string
	outcome        string
}

type runtimeSlotConfig struct {
	runtime        *BuiltRuntime
	session        adksession.Session
	userID         string
	sessionID      string
	archiveID      string
	generation     uint64
	effectiveModel string
	outcome        string
}

// RuntimeSlot owns one immutable runtime value and only mutable publication,
// lease, and drain bookkeeping.
type RuntimeSlot struct {
	value runtimeSlotValue
	owner *runtimeOwner

	mu                 sync.Mutex
	leases             int
	retired            bool
	ownerReleased      bool
	publicationClaimed bool
	published          bool
	drainErr           error
	drained            chan struct{}
}

func newRuntimeSlot(config runtimeSlotConfig) *RuntimeSlot {
	var runtime BuiltRuntime
	if config.runtime != nil {
		runtime = *config.runtime
	}
	value := runtimeSlotValue{
		runtime:        runtime,
		session:        config.session,
		userID:         config.userID,
		sessionID:      config.sessionID,
		archiveID:      config.archiveID,
		generation:     config.generation,
		effectiveModel: config.effectiveModel,
		outcome:        config.outcome,
	}
	if config.session != nil {
		value.appName = config.session.AppName()
		value.userID = config.session.UserID()
		value.sessionID = config.session.ID()
	}
	if value.appName == "" {
		value.appName = runtime.AppName
	}
	return newRuntimeSlotWithOwner(value, newRuntimeOwner(runtime.Close))
}

func newRuntimeSlotWithOwner(value runtimeSlotValue, owner *runtimeOwner) *RuntimeSlot {
	return &RuntimeSlot{value: value, owner: owner, drained: make(chan struct{})}
}

func newPublishedRuntimeSlot(config runtimeSlotConfig) *RuntimeSlot {
	slot := newRuntimeSlot(config)
	slot.markPublished()
	return slot
}

func (s *RuntimeSlot) markPublished() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.published = true
	s.mu.Unlock()
}

// sharedSuccessor creates another immutable slot over the same aggregate
// runtime. The caller still owns publication or abort of the returned slot.
func (s *RuntimeSlot) sharedSuccessor(generation uint64, effectiveModel, outcome string) (*RuntimeSlot, error) {
	if s == nil {
		return nil, ErrRuntimeUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired || s.owner == nil || !s.owner.retain() {
		return nil, ErrRuntimeUnavailable
	}
	value := s.value
	value.generation = generation
	value.effectiveModel = effectiveModel
	value.outcome = outcome
	return newRuntimeSlotWithOwner(value, s.owner), nil
}

func (s *RuntimeSlot) sharedModelSuccessor(effectiveModel, outcome string) (*RuntimeSlot, error) {
	if s == nil || s.value.generation == math.MaxUint64 {
		return nil, ErrRuntimePublicationInvalid
	}
	return s.sharedSuccessor(s.value.generation+1, effectiveModel, outcome)
}

// independentModelSuccessor wraps a replacement runtime that was built over
// the incumbent's exact ADK identity. Ownership remains with the returned
// unpublished slot (or with the caller when validation fails).
func (s *RuntimeSlot) independentModelSuccessor(runtime *BuiltRuntime, session adksession.Session, effectiveModel, outcome string) (*RuntimeSlot, error) {
	if s == nil || runtime == nil || s.value.generation == math.MaxUint64 || !sameRuntimeSessionIdentity(s.value, runtime, session) ||
		runtime.RuntimeSnapshotID != s.value.runtime.RuntimeSnapshotID ||
		runtime.ProviderBindingJSON != s.value.runtime.ProviderBindingJSON {
		return nil, ErrRuntimePublicationInvalid
	}
	return newRuntimeSlot(runtimeSlotConfig{
		runtime: runtime, session: session, archiveID: s.value.archiveID,
		generation: s.value.generation + 1, effectiveModel: effectiveModel, outcome: outcome,
	}), nil
}

func sameRuntimeSessionIdentity(current runtimeSlotValue, runtime *BuiltRuntime, session adksession.Session) bool {
	return sameIdentityObject(runtime.SessionSvc, current.runtime.SessionSvc) && sameIdentityObject(session, current.session) &&
		runtime.AppName == current.appName && session != nil &&
		session.AppName() == current.appName && session.UserID() == current.userID && session.ID() == current.sessionID
}

func sameIdentityObject(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	return leftValue.Type() == rightValue.Type() && leftValue.Type().Comparable() && leftValue.Interface() == rightValue.Interface()
}

func (s *RuntimeSlot) acquire() (*RuntimeLease, error) {
	if s == nil {
		return nil, ErrRuntimeUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired {
		return nil, ErrRuntimeUnavailable
	}
	s.leases++
	return &RuntimeLease{slot: s}, nil
}

func (s *RuntimeSlot) claimPublication() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired || s.ownerReleased || s.owner == nil || s.leases != 0 || s.publicationClaimed || s.published {
		return false
	}
	s.publicationClaimed = true
	return true
}

func (s *RuntimeSlot) releasePublicationClaim(published bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.publicationClaimed {
		s.publicationClaimed = false
		s.published = published
	}
	s.mu.Unlock()
}

func (s *RuntimeSlot) abortUnpublished(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.publicationClaimed || s.published {
		s.mu.Unlock()
		return nil
	}
	s.retired = true
	releaseOwner := s.leases == 0 && !s.ownerReleased
	if releaseOwner {
		s.ownerReleased = true
	}
	s.mu.Unlock()
	if releaseOwner {
		s.releaseOwner()
	}
	return s.waitDrain(ctx)
}

func (s *RuntimeSlot) releaseLease() {
	s.mu.Lock()
	if s.leases > 0 {
		s.leases--
	}
	releaseOwner := s.retired && s.leases == 0 && !s.ownerReleased
	if releaseOwner {
		s.ownerReleased = true
	}
	s.mu.Unlock()
	if releaseOwner {
		s.releaseOwner()
	}
}

func (s *RuntimeSlot) drain(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.beginDrain()
	return s.waitDrain(ctx)
}

func (s *RuntimeSlot) beginDrain() {
	s.mu.Lock()
	s.retired = true
	releaseOwner := s.leases == 0 && !s.ownerReleased
	if releaseOwner {
		s.ownerReleased = true
	}
	s.mu.Unlock()
	if releaseOwner {
		s.releaseOwner()
	}
}

func (s *RuntimeSlot) waitDrain(ctx context.Context) error {
	s.mu.Lock()
	drained := s.drained
	s.mu.Unlock()
	select {
	case <-drained:
		s.mu.Lock()
		err := s.drainErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ErrRuntimeCleanupIncomplete
	}
}

func (s *RuntimeSlot) releaseOwner() {
	wait := closedSignal()
	final := false
	if s.owner != nil {
		wait, final = s.owner.release()
	}
	go func() {
		<-wait
		var err error
		if final && s.owner != nil {
			err = s.owner.result()
		}
		s.mu.Lock()
		s.drainErr = err
		close(s.drained)
		s.mu.Unlock()
	}()
}

type runtimeOwner struct {
	mu       sync.Mutex
	refs     int
	closeFn  func() error
	closeErr error
	closed   chan struct{}
}

func newRuntimeOwner(closeFn func() error) *runtimeOwner {
	return &runtimeOwner{refs: 1, closeFn: closeFn, closed: make(chan struct{})}
}

func (o *runtimeOwner) retain() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.refs == 0 {
		return false
	}
	o.refs++
	return true
}

func (o *runtimeOwner) release() (<-chan struct{}, bool) {
	o.mu.Lock()
	if o.refs == 0 {
		closed := o.closed
		o.mu.Unlock()
		return closed, true
	}
	o.refs--
	if o.refs > 0 {
		o.mu.Unlock()
		return closedSignal(), false
	}
	closeFn := o.closeFn
	o.mu.Unlock()

	go func() {
		var err error
		if closeFn != nil && closeFn() != nil {
			err = ErrRuntimeCloseFailed
		}
		o.mu.Lock()
		o.closeErr = err
		close(o.closed)
		o.mu.Unlock()
	}()
	return o.closed, true
}

func (o *runtimeOwner) result() error {
	if o == nil {
		return nil
	}
	<-o.closed
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

func closedSignal() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
