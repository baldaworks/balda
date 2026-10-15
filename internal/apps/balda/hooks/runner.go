package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

const (
	maxEventBytes        = 256 << 10
	maxOutputBytes       = 64 << 10
	maxContextBytes      = 32 << 10
	maxIdentityBytes     = 1024
	maxInvocationEntries = 256
	maxGlobalEntries     = 4096
	deduplicationTTL     = 10 * time.Minute
)

// RecordReader is the runtime's read-only configuration port.
type RecordReader interface {
	Load(ctx context.Context) (hookcmd.Record, error)
}

// ExecutionSnapshotReader selects the effective definitions for one event.
type ExecutionSnapshotReader interface {
	LoadExecutionSnapshot(ctx context.Context) (hookcmd.ExecutionSnapshot, error)
}

type managedSnapshotReader struct {
	store RecordReader
}

// ManagedExecutionSnapshotReader adapts the existing managed-hook record to
// the combined execution snapshot contract.
func ManagedExecutionSnapshotReader(store RecordReader) ExecutionSnapshotReader {
	return managedSnapshotReader{store: store}
}

func (r managedSnapshotReader) LoadExecutionSnapshot(ctx context.Context) (hookcmd.ExecutionSnapshot, error) {
	if r.store == nil {
		return hookcmd.ExecutionSnapshot{}, hookcmd.ErrUnavailable
	}
	record, err := r.store.Load(ctx)
	if err != nil {
		return hookcmd.ExecutionSnapshot{}, err
	}
	if record.Validate() != nil {
		return hookcmd.ExecutionSnapshot{}, hookcmd.ErrUnavailable
	}
	snapshot := hookcmd.ExecutionSnapshot{Definitions: make([]hookcmd.EffectiveDefinition, 0, len(record.Definitions))}
	for _, definition := range record.Definitions {
		snapshot.Definitions = append(snapshot.Definitions, hookcmd.EffectiveDefinition{
			ID:         "managed/" + definition.Name,
			Definition: definition,
		})
	}
	return snapshot, nil
}

// Runner executes a deterministic snapshot of matching administrator hooks.
type Runner struct {
	snapshots ExecutionSnapshotReader
	gate      chan struct{}
	dedup     coalescingCache
}

// NewRunner binds runtime execution to a combined effective hook view.
func NewRunner(snapshots ExecutionSnapshotReader) *Runner {
	return &Runner{
		snapshots: snapshots,
		gate:      make(chan struct{}, 16),
		dedup:     newCoalescingCache(),
	}
}

// NewManagedRunner binds runtime execution to the existing managed-hook store.
func NewManagedRunner(store RecordReader) *Runner {
	return NewRunner(ManagedExecutionSnapshotReader(store))
}

// Run executes enabled hooks in effective identity order and fails closed on malformed output.
func (r *Runner) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if r == nil || r.snapshots == nil {
		return hookcmd.Result{}, hookcmd.ErrUnavailable
	}
	if !event.Name.Valid() || event.SessionID == "" || !filepath.IsAbs(event.CWD) {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	if event.Name.ToolEvent() && (event.ToolName == "" || len(event.ToolName) > 256 || len(event.SessionID) > maxIdentityBytes || len(event.InvocationID) > maxIdentityBytes || len(event.ToolCallID) > maxIdentityBytes || !json.Valid(event.ToolInput)) {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	key, eligible := deduplicationKeyFor(event)
	if eligible {
		return r.dedup.run(ctx, key, func() (hookcmd.Result, error) {
			return r.runOnce(ctx, event)
		})
	}
	return r.runOnce(ctx, event)
}

func (r *Runner) runOnce(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	snapshot, err := r.snapshots.LoadExecutionSnapshot(ctx)
	if err != nil {
		return hookcmd.Result{}, err
	}
	definitions := slices.Clone(snapshot.Definitions)
	for i := range definitions {
		definitions[i].Definition.Args = slices.Clone(definitions[i].Definition.Args)
	}
	if !validExecutionSnapshot(definitions) {
		return hookcmd.Result{}, hookcmd.ErrUnavailable
	}
	slices.SortFunc(definitions, func(a, b hookcmd.EffectiveDefinition) int { return strings.Compare(a.ID, b.ID) })
	var combined hookcmd.Result
	for _, effective := range definitions {
		definition := effective.Definition
		if !definition.Enabled || definition.Event != event.Name || definition.Provider != "" && definition.Provider != event.Provider {
			continue
		}
		matcher, err := regexp.Compile(definition.Matcher)
		if err != nil {
			return hookcmd.Result{}, hookcmd.ErrUnavailable
		}
		match := event.Source
		if event.Name.ToolEvent() {
			match = event.ToolName
		}
		if !matcher.MatchString(match) {
			continue
		}
		result, err := r.execute(ctx, definition, event)
		if err != nil {
			return hookcmd.Result{}, err
		}
		if result.Decision == hookcmd.DecisionDeny {
			return hookcmd.Result{}, &hookcmd.DeniedError{Hook: effective.ID, Reason: result.Reason}
		}
		if result.AdditionalContext != "" {
			if event.Name != hookcmd.UserPromptSubmit && !event.Name.ToolEvent() {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			addedBytes := len(result.AdditionalContext)
			if combined.AdditionalContext != "" {
				addedBytes++ // Separator between context contributions.
			}
			if len(combined.AdditionalContext)+addedBytes > maxContextBytes {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			if combined.AdditionalContext != "" {
				combined.AdditionalContext += "\n"
			}
			combined.AdditionalContext += result.AdditionalContext
		}
		if len(result.UpdatedInput) != 0 {
			if event.Name != hookcmd.PreToolUse || !jsonObject(result.UpdatedInput) {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			combined.UpdatedInput = slices.Clone(result.UpdatedInput)
			event.ToolInput = slices.Clone(result.UpdatedInput)
		}
		if len(result.UpdatedOutput) != 0 {
			if event.Name != hookcmd.PostToolUse || len(result.UpdatedOutput) > maxOutputBytes || !jsonObject(result.UpdatedOutput) {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			combined.UpdatedOutput = slices.Clone(result.UpdatedOutput)
			event.ToolResponse = slices.Clone(result.UpdatedOutput)
		}
	}
	return combined, nil
}

func validExecutionSnapshot(definitions []hookcmd.EffectiveDefinition) bool {
	seen := make(map[string]struct{}, len(definitions))
	for _, effective := range definitions {
		if !validEffectiveDefinition(effective) {
			return false
		}
		if _, ok := seen[effective.ID]; ok {
			return false
		}
		seen[effective.ID] = struct{}{}
	}
	return true
}

func validEffectiveDefinition(effective hookcmd.EffectiveDefinition) bool {
	if effective.Definition.Validate() != nil {
		return false
	}
	name := effective.Definition.Name
	if effective.ID == "managed/"+name {
		return effective.PluginID == "" && effective.PluginRevision == ""
	}
	if effective.PluginID == "" || strings.Contains(effective.PluginID, "/") || effective.PluginRevision == "" {
		return false
	}
	return effective.ID == "plugin/"+effective.PluginID+"/"+name
}

func jsonObject(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// ReleaseInvocation removes completed decisions for one invocation. An active
// decision is removed as soon as its single execution finishes.
func (r *Runner) ReleaseInvocation(sessionID, invocationID string) {
	if r != nil {
		r.dedup.releaseInvocation(sessionID, invocationID)
	}
}

// ReleaseSession removes completed decisions for one session. An active
// decision is removed as soon as its single execution finishes.
func (r *Runner) ReleaseSession(sessionID string) {
	if r != nil {
		r.dedup.releaseSession(sessionID)
	}
}

func (r *Runner) execute(ctx context.Context, definition hookcmd.Definition, event hookcmd.Event) (hookcmd.Result, error) {
	if definition.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(definition.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	return runCommand(ctx, definition, event)
}

func runCommand(ctx context.Context, definition hookcmd.Definition, event hookcmd.Event) (hookcmd.Result, error) {
	input, err := json.Marshal(event)
	if err != nil || len(input) > maxEventBytes {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(runCtx, definition.Command, definition.Args...)
	command.Dir = event.CWD
	command.Stdin = bytes.NewReader(input)
	stdout, stderr := &boundedOutput{cancel: cancel}, &boundedOutput{cancel: cancel}
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = time.Second
	configureProcess(command)
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 && runCtx.Err() == nil {
			return hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: "Hook rejected the event."}, nil
		}
		return hookcmd.Result{}, fmt.Errorf("%w: %s", hookcmd.ErrExecution, definition.Name)
	}
	if runCtx.Err() != nil {
		return hookcmd.Result{}, fmt.Errorf("%w: %s", hookcmd.ErrExecution, definition.Name)
	}
	if len(bytes.TrimSpace(stdout.Bytes())) == 0 {
		return hookcmd.Result{}, nil
	}
	if bytes.TrimSpace(stdout.Bytes())[0] != '{' {
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var result hookcmd.Result
	if err := decoder.Decode(&result); err != nil {
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	if result.Decision != "" && result.Decision != hookcmd.DecisionAllow && result.Decision != hookcmd.DecisionDeny || len(result.Reason) > 1024 || len(result.AdditionalContext) > maxContextBytes {
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	if result.Decision == hookcmd.DecisionDeny && strings.TrimSpace(result.Reason) == "" {
		result.Reason = "Hook rejected the event."
	}
	return result, nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > maxOutputBytes-b.buffer.Len() {
		b.cancel()
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(data)
}
