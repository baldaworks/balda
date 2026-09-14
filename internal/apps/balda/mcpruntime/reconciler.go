// Package mcpruntime owns desired MCP process state and observed health.
package mcpruntime

import (
	"context"
	"errors"
	"fmt"
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

// Projector applies a ready instance to provider runtime configuration.
// Project and Remove must honor context cancellation and deadlines.
type Projector interface {
	Project(ctx context.Context, key InstanceKey, config LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error)
	Remove(ctx context.Context, key InstanceKey) error
}

// HealthState is observed process state and never participates in snapshot identity.
type HealthState string

const (
	HealthDisabled HealthState = "disabled"
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
	refs       int
	desired    bool
	projected  bool
	closed     bool
}

// Reconciler owns revision coexistence, drain, shutdown, and health.
type Reconciler struct {
	mu        sync.Mutex
	resolver  LaunchResolver
	launcher  Launcher
	projector Projector
	limits    Limits
	instances map[InstanceKey]*managedInstance
	omissions []Health
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

// Reconcile starts new revisions before draining old revisions.
func (r *Reconciler) Reconcile(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) []Health {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	for _, current := range r.instances {
		current.desired = false
	}
	for _, descriptor := range desired {
		key := keyFromDescriptor(descriptor)
		if current, ok := r.instances[key]; ok {
			current.desired = true
			switch current.health.State {
			case HealthFailed:
				delete(r.instances, key)
			case HealthStopping:
				current.desired = false
				if err := r.cleanupBoundedLocked(ctx, key, current); err != nil {
					current.desired = true
					continue
				}
			case HealthDegraded:
				outcome, err := r.projector.Project(ctx, key, cloneLaunchConfig(current.config))
				current.health.Outcome = outcome
				if err == nil && (outcome == runtimecatalogcmd.MCPProjectionApplied || outcome == runtimecatalogcmd.MCPProjectionNewRuntimesOnly) {
					current.health.State, current.health.ErrorClass = HealthReady, ""
				}
				continue
			default:
				continue
			}
		}
		r.startLocked(ctx, key, descriptor)
	}
	for key, current := range r.instances {
		if current.desired || current.refs > 0 {
			continue
		}
		if r.replacementPendingLocked(key) {
			continue
		}
		r.stopLocked(ctx, key, current)
	}
	return r.healthLocked()
}

func (r *Reconciler) replacementPendingLocked(oldKey InstanceKey) bool {
	found := false
	for key, current := range r.instances {
		if !current.desired || key.Source != oldKey.Source || key.Name != oldKey.Name || key.Revision == oldKey.Revision {
			continue
		}
		found = true
		if current.health.State == HealthReady {
			return false
		}
	}
	return found
}

func (r *Reconciler) startLocked(ctx context.Context, key InstanceKey, descriptor runtimecatalogcmd.MCPServerDescriptor) {
	current := &managedInstance{descriptor: descriptor, desired: true, health: Health{Key: key, State: HealthStarting}}
	r.instances[key] = current
	config, err := r.resolver.ResolveLaunch(ctx, descriptor)
	if err != nil {
		current.health.State, current.health.ErrorClass = HealthFailed, "resolve"
		return
	}
	instance, err := r.launcher.Start(ctx, key, cloneLaunchConfig(config))
	if err != nil {
		current.health.State, current.health.ErrorClass = HealthFailed, "start"
		return
	}
	current.instance = instance
	tools := cloneTools(instance.Tools())
	if len(tools) > r.limits.MaxTools || toolMetadataBytes(tools) > r.limits.MaxToolMetadataBytes {
		if err := instance.Close(ctx); err != nil {
			current.health.State, current.health.ErrorClass = HealthStopping, errorClassStop
			return
		}
		current.instance, current.closed = nil, true
		current.health.State, current.health.ErrorClass = HealthFailed, "tool_limit"
		return
	}
	current.projected = true
	outcome, err := r.projector.Project(ctx, key, cloneLaunchConfig(config))
	current.config, current.tools = cloneLaunchConfig(config), tools
	current.health.ToolCount, current.health.Outcome = len(tools), outcome
	if err != nil || (outcome != runtimecatalogcmd.MCPProjectionApplied && outcome != runtimecatalogcmd.MCPProjectionNewRuntimesOnly) {
		current.health.State, current.health.ErrorClass = HealthDegraded, "projection"
		return
	}
	current.health.State = HealthReady
}

// Acquire pins ready matching tools for one turn.
func (r *Reconciler) Acquire(keys []InstanceKey) ([]Tool, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var selected []*managedInstance
	var tools []Tool
	for _, key := range keys {
		current, ok := r.instances[key]
		if !ok || current.health.State != HealthReady {
			return nil, nil, fmt.Errorf("MCP revision unavailable: %s", key.Name)
		}
		selected = append(selected, current)
		for _, tool := range current.tools {
			tool.Key = key
			tools = append(tools, tool)
		}
	}
	for _, current := range selected {
		current.refs++
	}
	var once sync.Once
	return append([]Tool(nil), tools...), func() {
		once.Do(func() { r.release(selected) })
	}, nil
}

// AcquireDescriptors ensures and pins exact revision instances for one turn.
// Non-current revisions are stopped again when the turn releases them.
func (r *Reconciler) AcquireDescriptors(ctx context.Context, descriptors []runtimecatalogcmd.MCPServerDescriptor) ([]InstanceKey, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(descriptors) > r.limits.MaxServers {
		return nil, nil, errors.New("MCP server selection exceeds configured limit")
	}
	selected := make([]*managedInstance, 0, len(descriptors))
	keys := make([]InstanceKey, 0, len(descriptors))
	for _, descriptor := range descriptors {
		key := InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
		current, ok := r.instances[key]
		if !ok {
			r.startLocked(ctx, key, descriptor)
			current = r.instances[key]
			current.desired = false
		}
		if current.health.State != HealthReady {
			for _, acquired := range selected {
				acquired.refs--
			}
			r.cleanupEphemeralLocked(ctx)
			return nil, nil, fmt.Errorf("MCP revision unavailable: %s", key.Name)
		}
		current.refs++
		selected = append(selected, current)
		keys = append(keys, key)
	}
	var once sync.Once
	return keys, func() {
		once.Do(func() { r.release(selected) })
	}, nil
}

func (r *Reconciler) cleanupEphemeralLocked(ctx context.Context) {
	for key, current := range r.instances {
		if !current.desired && current.refs == 0 {
			r.stopLocked(ctx, key, current)
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
	for key, current := range r.instances {
		if !current.desired && current.refs == 0 {
			r.stopLocked(context.Background(), key, current)
		}
	}
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
	var errs []error
	for key, current := range r.instances {
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
	current.health.State = HealthStopping
	var errs []error
	if current.projected {
		if err := r.projector.Remove(ctx, key); err != nil {
			errs = append(errs, errors.New("remove MCP projection"))
		} else {
			current.projected = false
		}
	}
	if current.instance != nil && !current.closed {
		if err := current.instance.Close(ctx); err != nil {
			errs = append(errs, errors.New("stop MCP instance"))
		} else {
			current.closed = true
		}
	}
	if !current.projected && (current.instance == nil || current.closed) {
		delete(r.instances, key)
	}
	err := errors.Join(errs...)
	if err != nil {
		current.health.ErrorClass = errorClassStop
	}
	return err
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
