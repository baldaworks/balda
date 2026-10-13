// Package mcpruntime owns desired MCP process state and observed health.
package mcpruntime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// InstanceKey pins one server process to source revision identity.
type InstanceKey struct {
	Source   runtimecatalogcmd.SourceID
	Revision runtimecatalogcmd.RevisionID
	Name     string
}

// LaunchConfig is resolved only at the launch boundary and must never be persisted.
type LaunchConfig struct {
	Transport  string
	Command    string
	Args       []string
	Env        map[string]string
	WorkingDir string
	URL        string
	Headers    map[string]string
	// InheritEnvironment is explicit for configured/managed stdio. Plugin
	// discovery keeps its isolated environment and allowlist policy.
	InheritEnvironment bool
	// EnforceHTTPOrigin scopes resolved managed credentials to their endpoint.
	EnforceHTTPOrigin bool
	// ObservedFailure reads trusted private evidence from a fixed-upstream
	// adapter. It is neither wire metadata nor part of a catalog snapshot.
	ObservedFailure func() FailureReason `json:"-"`
}

// Tool is bounded observed tool identity; raw schemas and results stay in the instance.
type Tool struct {
	Key         InstanceKey
	Name        string
	Description string
}

// Instance is one started, handshaken, revision-keyed MCP connection.
// Close must honor context cancellation and deadlines.
type Instance interface {
	Tools() []Tool
	Close(ctx context.Context) error
}

// LaunchResolver resolves package paths and credentials at launch time only.
type LaunchResolver interface {
	ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error)
}

// Launcher starts and handshakes one MCP instance without a shell.
type Launcher interface {
	Start(ctx context.Context, key InstanceKey, config LaunchConfig) (Instance, error)
}

// Projector installs exact configuration independently of transport readiness.
// Project and Remove must honor context cancellation and deadlines.
type Projector interface {
	Project(ctx context.Context, key InstanceKey, config LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error)
	Remove(ctx context.Context, key InstanceKey) error
}

// HealthState is observed process state and never participates in snapshot identity.
type HealthState string

const (
	HealthDisabled HealthState = "disabled"
	HealthUnknown  HealthState = "unknown"
	HealthStarting HealthState = "starting"
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthFailed   HealthState = "failed"
	HealthStopping HealthState = "stopping"
	errorClassStop             = "stop"
)

// Health is a bounded, non-secret observed status.
type Health struct {
	Key        InstanceKey
	State      HealthState
	Outcome    runtimecatalogcmd.MCPProjectionOutcome
	ToolCount  int
	ErrorClass string
}

// Limits bound one desired-state reconciliation.
type Limits struct {
	MaxServers           int
	MaxTools             int
	MaxToolMetadataBytes int
	StopTimeout          time.Duration
}

// DefaultLimits returns conservative process and tool limits.
func DefaultLimits() Limits {
	return Limits{MaxServers: 32, MaxTools: 256, MaxToolMetadataBytes: 256 << 10, StopTimeout: 5 * time.Second}
}

type managedInstance struct {
	descriptor runtimecatalogcmd.MCPServerDescriptor
	config     LaunchConfig
	instance   Instance
	tools      []Tool
	health     Health
	failure    FailureReason
	refs       int
	desired    bool
	projected  bool
	closed     bool
	valid      bool
	installing chan struct{}
	stopping   chan struct{}
	cancel     context.CancelFunc
	retiring   bool
	stopErr    error
}

// Reconciler owns revision coexistence, drain, shutdown, and health.
type Reconciler struct {
	mu          sync.Mutex
	resolver    LaunchResolver
	launcher    Launcher
	projector   Projector
	limits      Limits
	instances   map[InstanceKey]*managedInstance
	omissions   []Health
	stopped     bool
	generation  uint64
	desiredKeys map[InstanceKey]bool
}

// New creates an MCP desired-state reconciler.
func New(resolver LaunchResolver, launcher Launcher, projector Projector, limits Limits) (*Reconciler, error) {
	if resolver == nil || launcher == nil || projector == nil {
		return nil, errors.New("MCP resolver, launcher, and projector are required")
	}
	defaults := DefaultLimits()
	if limits.MaxServers == 0 {
		limits.MaxServers = defaults.MaxServers
	}
	if limits.MaxTools == 0 {
		limits.MaxTools = defaults.MaxTools
	}
	if limits.MaxToolMetadataBytes == 0 {
		limits.MaxToolMetadataBytes = defaults.MaxToolMetadataBytes
	}
	if limits.StopTimeout == 0 {
		limits.StopTimeout = defaults.StopTimeout
	}
	if limits.MaxServers < 0 || limits.MaxTools < 0 || limits.MaxToolMetadataBytes < 0 || limits.StopTimeout < 0 {
		return nil, errors.New("MCP limits must be positive")
	}
	return &Reconciler{resolver: resolver, launcher: launcher, projector: projector, limits: limits, instances: make(map[InstanceKey]*managedInstance)}, nil
}

// Reconcile installs local exact configurations before draining old revisions.
func (r *Reconciler) Reconcile(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) []Health {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || ctx.Err() != nil {
		return r.healthLocked()
	}
	r.generation++
	generation := r.generation
	desired := orderedDescriptors(snapshot)
	r.omissions = nil
	if len(desired) > r.limits.MaxServers {
		for _, descriptor := range desired[r.limits.MaxServers:] {
			r.omissions = append(r.omissions, Health{
				Key: keyFromDescriptor(descriptor), State: HealthFailed, ErrorClass: "server_limit",
			})
		}
		desired = desired[:r.limits.MaxServers]
	}
	r.desiredKeys = make(map[InstanceKey]bool, len(desired))
	for _, descriptor := range desired {
		r.desiredKeys[keyFromDescriptor(descriptor)] = true
	}
	for key, current := range r.instances {
		current.desired = r.desiredKeys[key]
	}
	for _, descriptor := range desired {
		if r.stopped || r.generation != generation || ctx.Err() != nil {
			break
		}
		key := keyFromDescriptor(descriptor)
		if current := r.instances[key]; current != nil && current.retiring {
			if err := r.cleanupBoundedLocked(ctx, key, current); err != nil {
				continue
			}
			if r.stopped || r.generation != generation || ctx.Err() != nil {
				break
			}
		}
		if current, ok := r.instances[key]; ok {
			current.desired = true
			if current.valid || current.health.State != HealthFailed || current.refs != 0 {
				continue
			}
			if err := r.cleanupBoundedLocked(ctx, key, current); err != nil {
				continue
			}
			if r.stopped || r.generation != generation || ctx.Err() != nil {
				break
			}
		}
		r.installLocked(ctx, key, descriptor, true, 0)
	}
	if r.stopped || r.generation != generation {
		return r.healthLocked()
	}
	for key, current := range r.instanceSnapshotLocked() {
		if current.desired || current.refs > 0 {
			continue
		}
		r.stopLocked(ctx, key, current)
	}
	return r.healthLocked()
}

func (r *Reconciler) installLocked(ctx context.Context, key InstanceKey, descriptor runtimecatalogcmd.MCPServerDescriptor, desired bool, refs int) *managedInstance {
	if r.stopped || ctx.Err() != nil {
		return nil
	}
	if r.desiredKeys != nil {
		desired = r.desiredKeys[key]
	}
	current := &managedInstance{descriptor: descriptor, desired: desired, refs: refs, installing: make(chan struct{}), health: Health{Key: key, State: HealthStarting}}
	r.instances[key] = current
	ctx, cancel := context.WithCancel(ctx)
	current.cancel = cancel
	r.mu.Unlock()
	config, err := r.resolver.ResolveLaunch(ctx, descriptor)
	projected := err == nil // Resolution may install a private local endpoint.
	if err == nil && (descriptor.ID.Kind != runtimecatalogcmd.ContributionKindMCPServer || descriptor.ID.Name != descriptor.Name || key.Revision == "" || config.Transport != descriptor.Transport) {
		err = runtimecatalogcmd.ErrRevisionUnavailable
	}
	var outcome runtimecatalogcmd.MCPProjectionOutcome
	if err == nil {
		projected = true // Remove also rolls back a partially applied projection.
		outcome, err = r.projector.Project(ctx, key, cloneLaunchConfig(config))
		if err == nil && outcome != runtimecatalogcmd.MCPProjectionApplied && outcome != runtimecatalogcmd.MCPProjectionNewRuntimesOnly {
			err = errors.New("MCP projection unavailable")
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	valid := err == nil
	var instance Instance
	var tools []Tool
	state := HealthUnknown
	errorClass := "configuration"
	// Existing stdio discovery remains here until the process-aware worker
	// takes ownership. Remote configuration never performs discovery.
	if valid && config.Transport == "stdio" {
		errorClass = "start"
		instance, err = r.launcher.Start(ctx, key, cloneLaunchConfig(config))
		if err == nil {
			tools = cloneTools(instance.Tools())
			if len(tools) > r.limits.MaxTools || toolMetadataBytes(tools) > r.limits.MaxToolMetadataBytes {
				valid, errorClass = false, "tool_limit"
				err = errors.New("MCP tool metadata exceeds configured limits")
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), r.limits.StopTimeout)
				if instance.Close(cleanupCtx) == nil {
					instance = nil
				}
				if r.projector.Remove(cleanupCtx, key) == nil {
					projected = false
				}
				cleanupCancel()
				tools = nil
			}
			state = HealthReady
		}
	}
	cancel()
	r.mu.Lock()
	if r.stopped {
		valid = false
		if err == nil {
			err = errors.New("MCP runtime is stopped")
		}
	}
	current.projected, current.valid, current.config, current.health.Outcome = projected, valid, cloneLaunchConfig(config), outcome
	current.instance, current.tools, current.health.ToolCount = instance, tools, len(tools)
	current.health.State = state
	if err != nil {
		current.health.State, current.health.ErrorClass, current.failure = HealthFailed, errorClass, launchReason(err)
	}
	if current.retiring {
		current.health.State = HealthStopping
	}
	close(current.installing)
	current.installing, current.cancel = nil, nil
	return current
}

// Acquire leases installed configurations and returns only observed tool metadata.
func (r *Reconciler) Acquire(keys []InstanceKey) ([]Tool, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil, nil, errors.New("MCP runtime is stopped")
	}
	var selected []*managedInstance
	var tools []Tool
	var failures []error
	for _, key := range keys {
		current, ok := r.instances[key]
		if !ok || !current.valid || current.installing != nil || current.health.State == HealthStopping {
			failures = append(failures, attachmentFailure(key, current))
			continue
		}
		selected = append(selected, current)
		for _, tool := range current.tools {
			tool.Key = key
			tools = append(tools, tool)
		}
	}
	if len(failures) != 0 {
		return nil, nil, errors.Join(failures...)
	}
	for _, current := range selected {
		current.refs++
	}
	var once sync.Once
	return append([]Tool(nil), tools...), func() {
		once.Do(func() { r.release(selected) })
	}, nil
}

// AcquireDescriptors installs and leases every exact configuration locally.
// Non-current revisions are stopped again when that owner releases them.
func (r *Reconciler) AcquireDescriptors(ctx context.Context, descriptors []runtimecatalogcmd.MCPServerDescriptor) ([]InstanceKey, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if r.stopped {
		return nil, nil, errors.New("MCP runtime is stopped")
	}
	if len(descriptors) > r.limits.MaxServers {
		return nil, nil, errors.New("MCP server selection exceeds configured limit")
	}
	selected := make([]*managedInstance, 0, len(descriptors))
	keys := make([]InstanceKey, 0, len(descriptors))
	var failures []error
	for _, descriptor := range descriptors {
		if r.stopped {
			failures = append(failures, errors.New("MCP runtime is stopped"))
			break
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		key := InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
		current, ok := r.instances[key]
		if !ok {
			current = r.installLocked(ctx, key, descriptor, false, 1)
		} else {
			if current.retiring {
				failures = append(failures, attachmentFailure(key, current))
				continue
			}
			current.refs++
		}
		if current == nil {
			failures = append(failures, errors.New("MCP configuration installation canceled"))
			break
		}
		selected = append(selected, current)
		if current.installing != nil {
			done := current.installing
			r.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
			}
			r.mu.Lock()
		}
		if r.stopped || r.instances[key] != current || !current.valid || current.retiring || current.descriptor.Transport != descriptor.Transport || current.descriptor.ConfigRef != descriptor.ConfigRef || current.descriptor.ID != descriptor.ID || descriptor.ID.Kind != runtimecatalogcmd.ContributionKindMCPServer || descriptor.ID.Name != descriptor.Name {
			failures = append(failures, attachmentFailure(key, current))
			continue
		}
		keys = append(keys, key)
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	if len(failures) != 0 {
		for _, current := range selected {
			current.refs--
		}
		r.cleanupSelectionLocked(context.WithoutCancel(ctx), selected)
		return nil, nil, errors.Join(failures...)
	}
	var once sync.Once
	return keys, func() {
		once.Do(func() { r.release(selected) })
	}, nil
}

func (r *Reconciler) cleanupSelectionLocked(ctx context.Context, selected []*managedInstance) {
	for _, current := range selected {
		if !current.desired && current.refs == 0 && !current.retiring {
			r.stopLocked(ctx, current.health.Key, current)
		}
	}
}

func (r *Reconciler) release(selected []*managedInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, current := range selected {
		if current.refs > 0 {
			current.refs--
		}
	}
	r.cleanupSelectionLocked(context.Background(), selected)
}

// Health returns a sorted non-secret observed snapshot.
func (r *Reconciler) Health() []Health {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthLocked()
}

// MCPServerReady reports observed readiness for one exact catalog identity.
func (r *Reconciler) MCPServerReady(source runtimecatalogcmd.SourceID, revision runtimecatalogcmd.RevisionID, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.instances[InstanceKey{Source: source, Revision: revision, Name: name}]
	return ok && current.health.State == HealthReady
}

// Shutdown closes every instance without changing catalog snapshots.
func (r *Reconciler) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	var errs []error
	for key, current := range r.instanceSnapshotLocked() {
		if err := r.cleanupBoundedLocked(ctx, key, current); err != nil {
			errs = append(errs, err)
		}
	}
	r.omissions = nil
	return errors.Join(errs...)
}

func (r *Reconciler) stopLocked(ctx context.Context, key InstanceKey, current *managedInstance) {
	_ = r.cleanupBoundedLocked(ctx, key, current)
}

func (r *Reconciler) cleanupBoundedLocked(ctx context.Context, key InstanceKey, current *managedInstance) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, r.limits.StopTimeout)
	defer cancel()
	return r.cleanupLocked(cleanupCtx, key, current)
}

func (r *Reconciler) cleanupLocked(ctx context.Context, key InstanceKey, current *managedInstance) error {
	if r.instances[key] != current {
		return nil
	}
	if current.stopping != nil {
		done := current.stopping
		r.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		r.mu.Lock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return current.stopErr
	}
	current.stopping = make(chan struct{})
	done := current.stopping
	defer func() { close(done); current.stopping = nil }()
	current.retiring = true
	current.health.State = HealthStopping
	if current.cancel != nil {
		current.cancel()
	}
	if current.installing != nil {
		installed := current.installing
		r.mu.Unlock()
		select {
		case <-installed:
		case <-ctx.Done():
		}
		r.mu.Lock()
		if ctx.Err() != nil {
			current.stopErr = ctx.Err()
			return ctx.Err()
		}
	}
	projected, instance, closed := current.projected, current.instance, current.closed
	r.mu.Unlock()
	var errs []error
	if projected {
		if err := r.projector.Remove(ctx, key); err != nil {
			errs = append(errs, errors.New("remove MCP projection"))
		} else {
			projected = false
		}
	}
	if instance != nil && !closed {
		if err := instance.Close(ctx); err != nil {
			errs = append(errs, errors.New("stop MCP instance"))
		} else {
			closed = true
		}
	}
	r.mu.Lock()
	current.projected, current.closed = projected, closed
	if !current.projected && (current.instance == nil || current.closed) {
		delete(r.instances, key)
	}
	err := errors.Join(errs...)
	current.stopErr = err
	if err != nil {
		current.health.ErrorClass = errorClassStop
	}
	return err
}

func (r *Reconciler) instanceSnapshotLocked() map[InstanceKey]*managedInstance {
	result := make(map[InstanceKey]*managedInstance, len(r.instances))
	for key, current := range r.instances {
		result[key] = current
	}
	return result
}

func (r *Reconciler) healthLocked() []Health {
	health := make([]Health, 0, len(r.instances)+len(r.omissions))
	for _, current := range r.instances {
		health = append(health, current.health)
	}
	health = append(health, r.omissions...)
	sort.Slice(health, func(i, j int) bool { return keyString(health[i].Key) < keyString(health[j].Key) })
	return health
}

func orderedDescriptors(snapshot runtimecatalogcmd.Snapshot) []runtimecatalogcmd.MCPServerDescriptor {
	result := make([]runtimecatalogcmd.MCPServerDescriptor, 0, len(snapshot.MCPServers))
	for _, descriptor := range snapshot.MCPServers {
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].ID, result[j].ID
		if left.Source.Kind != right.Source.Kind {
			return left.Source.Kind < right.Source.Kind
		}
		if left.Source.Name != right.Source.Name {
			return left.Source.Name < right.Source.Name
		}
		return left.Name < right.Name
	})
	return result
}

func keyFromDescriptor(descriptor runtimecatalogcmd.MCPServerDescriptor) InstanceKey {
	return InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
}

func keyString(key InstanceKey) string {
	return string(key.Source.Kind) + "\x00" + key.Source.Name + "\x00" + string(key.Revision) + "\x00" + key.Name
}

func cloneLaunchConfig(config LaunchConfig) LaunchConfig {
	config.Command = strings.TrimSpace(config.Command)
	config.Args = append([]string(nil), config.Args...)
	config.Env = cloneStrings(config.Env)
	config.Headers = cloneStrings(config.Headers)
	return config
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func toolMetadataBytes(tools []Tool) int {
	total := 0
	for _, tool := range tools {
		total += len(tool.Name) + len(tool.Description)
	}
	return total
}

func cloneTools(tools []Tool) []Tool { return append([]Tool(nil), tools...) }
