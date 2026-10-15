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
	maxEventBytes   = 256 << 10
	maxOutputBytes  = 64 << 10
	maxContextBytes = 32 << 10
)

// RecordReader is the runtime's read-only configuration port.
type RecordReader interface {
	Load(ctx context.Context) (hookcmd.Record, error)
}

// Runner executes a deterministic snapshot of matching administrator hooks.
type Runner struct {
	store RecordReader
	gate  chan struct{}
}

// NewRunner binds runtime execution to the shared configuration owner.
func NewRunner(store RecordReader) *Runner {
	return &Runner{store: store, gate: make(chan struct{}, 16)}
}

// Run executes enabled hooks in name order and fails closed on malformed output.
func (r *Runner) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	if r == nil || r.store == nil {
		return hookcmd.Result{}, hookcmd.ErrUnavailable
	}
	if !event.Name.Valid() || event.SessionID == "" || !filepath.IsAbs(event.CWD) {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	if event.Name.ToolEvent() && (event.ToolName == "" || len(event.ToolName) > 256 || !json.Valid(event.ToolInput)) {
		return hookcmd.Result{}, hookcmd.ErrInvalid
	}
	record, err := r.store.Load(ctx)
	if err != nil {
		return hookcmd.Result{}, err
	}
	if record.Validate() != nil {
		return hookcmd.Result{}, hookcmd.ErrUnavailable
	}
	definitions := slices.Clone(record.Definitions)
	slices.SortFunc(definitions, func(a, b hookcmd.Definition) int { return strings.Compare(a.Name, b.Name) })
	var combined hookcmd.Result
	for _, definition := range definitions {
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
			return hookcmd.Result{}, &hookcmd.DeniedError{Hook: definition.Name, Reason: result.Reason}
		}
		if result.AdditionalContext != "" {
			if event.Name != hookcmd.UserPromptSubmit && !event.Name.ToolEvent() {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			if len(combined.AdditionalContext)+len(result.AdditionalContext)+1 > maxContextBytes {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			if combined.AdditionalContext != "" {
				combined.AdditionalContext += "\n"
			}
			combined.AdditionalContext += result.AdditionalContext
		}
		if len(result.UpdatedInput) != 0 {
			if event.Name != hookcmd.PreToolUse || !json.Valid(result.UpdatedInput) || len(bytes.TrimSpace(result.UpdatedInput)) == 0 || bytes.TrimSpace(result.UpdatedInput)[0] != '{' {
				return hookcmd.Result{}, hookcmd.ErrExecution
			}
			combined.UpdatedInput = slices.Clone(result.UpdatedInput)
			event.ToolInput = slices.Clone(result.UpdatedInput)
		}
	}
	return combined, nil
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
	command := exec.CommandContext(ctx, definition.Command, definition.Args...)
	command.Dir = event.CWD
	command.Stdin = bytes.NewReader(input)
	stdout, stderr := &boundedOutput{}, &boundedOutput{}
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = time.Second
	configureProcess(command)
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 && ctx.Err() == nil {
			return hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: "Hook rejected the event."}, nil
		}
		return hookcmd.Result{}, fmt.Errorf("%w: %s", hookcmd.ErrExecution, definition.Name)
	}
	if ctx.Err() != nil {
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

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > maxOutputBytes-b.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(data)
}
