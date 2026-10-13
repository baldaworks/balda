package mcpruntime

import (
	"context"
	"time"
)

const stdioTransport = "stdio"

// Process is an owned discovery child, available before protocol readiness.
// Done closes only after the child has exited and has been reaped.
type Process interface {
	Discover(ctx context.Context) ([]Tool, error)
	Done() <-chan struct{}
	Stop(ctx context.Context) error
}

// ProcessLauncher owns exec, pipes, protocol setup and child reaping. A started
// child must be returned even if cancellation races with successful launch.
type ProcessLauncher interface {
	Launch(ctx context.Context, key InstanceKey, config LaunchConfig) (Process, error)
}

type restartTimer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type wallTimer struct{ *time.Timer }

func (t wallTimer) Chan() <-chan time.Time { return t.C }

type stdioPolicy struct {
	setupTimeout, stopTimeout, stableAfter time.Duration
	now                                    func() time.Time
	newTimer                               func(time.Duration) restartTimer
}

func defaultStdioPolicy() stdioPolicy {
	return stdioPolicy{setupTimeout: 10 * time.Second, stopTimeout: 5 * time.Second, stableAfter: time.Minute,
		now: time.Now, newTimer: func(delay time.Duration) restartTimer { return wallTimer{time.NewTimer(delay)} }}
}

type stdioWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (r *Reconciler) scheduleStdioLocked(key InstanceKey, current *managedInstance) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &stdioWorker{cancel: cancel, done: make(chan struct{})}
	current.worker = worker
	config, installed := cloneLaunchConfig(current.config), current.installing
	go func() {
		defer close(worker.done)
		select {
		case <-installed:
		case <-ctx.Done():
			return
		}
		r.runStdio(ctx, key, current, config)
	}()
}

func (r *Reconciler) runStdio(ctx context.Context, key InstanceKey, current *managedInstance, config LaunchConfig) {
	var delay time.Duration
	for ctx.Err() == nil {
		r.observeStdio(ctx, key, current, nil, HealthStarting, "")
		process, err := r.launcher.Launch(ctx, key, cloneLaunchConfig(config))
		stable := false
		errorClass := "launch"
		if process != nil {
			started := r.stdio.now()
			if err == nil && ctx.Err() == nil {
				errorClass = "discovery"
				setupCtx, cancel := context.WithTimeout(ctx, r.stdio.setupTimeout)
				tools, discoverErr := process.Discover(setupCtx)
				cancel()
				if len(tools) > r.limits.MaxTools || toolMetadataBytes(tools) > r.limits.MaxToolMetadataBytes {
					errorClass = "tool_limit"
				}
				if discoverErr == nil && len(tools) <= r.limits.MaxTools && toolMetadataBytes(tools) <= r.limits.MaxToolMetadataBytes && ctx.Err() == nil {
					r.observeStdio(ctx, key, current, tools, HealthReady, "")
					errorClass = "process"
					select {
					case <-process.Done():
					case <-ctx.Done():
					}
					stable = r.stdio.now().Sub(started) >= r.stdio.stableAfter
				}
			}
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.stdio.stopTimeout)
			_ = process.Stop(stopCtx)
			cancel()
			// Stop may return at its deadline after forcing termination. Keep
			// ownership until the concrete adapter reports actual reaping.
			<-process.Done()
		}
		if ctx.Err() != nil {
			return
		}
		r.observeStdio(ctx, key, current, nil, HealthFailed, errorClass)
		delay = restartDelay(delay, stable)
		timer := r.stdio.newTimer(delay)
		select {
		case <-timer.Chan():
		case <-ctx.Done():
			timer.Stop()
			return
		}
		timer.Stop()
	}
}

func restartDelay(previous time.Duration, stable bool) time.Duration {
	if stable || previous == 0 {
		return time.Second
	}
	return min(previous*2, 30*time.Second)
}

func (r *Reconciler) observeStdio(ctx context.Context, key InstanceKey, current *managedInstance, tools []Tool, state HealthState, errorClass string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil || r.stopped || r.instances[key] != current || current.retiring {
		return
	}
	current.tools, current.health.ToolCount = cloneTools(tools), len(tools)
	current.health.State, current.health.ErrorClass = state, errorClass
	if state == HealthFailed {
		current.failure = FailureUnavailable
	}
}
