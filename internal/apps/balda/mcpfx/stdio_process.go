package mcpfx

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Launch returns an owned stdio discovery child before MCP initialization.
// The provider's separate tool-execution children never enter this adapter.
func (*ClientLauncher) Launch(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Transport != transportStdio {
		return nil, &mcpruntime.LaunchError{Reason: mcpruntime.FailureUnavailable}
	}
	// The existing native shim preserves working-directory semantics and the
	// Windows kill-on-close process job. Environment policy stays with config.
	var err error
	config, err = stdioProviderLaunch(config)
	if err != nil {
		return nil, &mcpruntime.LaunchError{Reason: mcpruntime.FailureUnavailable}
	}
	processCtx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(processCtx, config.Command, config.Args...)
	command.Dir = config.WorkingDir
	command.Env = environment(config.Env)
	if config.InheritEnvironment {
		command.Env = append(os.Environ(), command.Env...)
	}
	command.Stderr = io.Discard
	command.Cancel = func() error { return signalOwnedStdio(command.Process) }
	command.WaitDelay = 5 * time.Second
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, &mcpruntime.LaunchError{Reason: mcpruntime.FailureUnavailable}
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, &mcpruntime.LaunchError{Reason: mcpruntime.FailureUnavailable}
	}
	stopLaunch := context.AfterFunc(ctx, cancel)
	err = command.Start()
	stopLaunch()
	if err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, launchFailure(ctx, nil)
	}
	p := &stdioProcess{key: key, command: command, cancel: cancel, input: input, output: output, exited: make(chan struct{}), stopped: make(chan struct{})}
	go func() { _ = command.Wait(); close(p.exited) }()
	// Even a canceled successful Start returns its handle for Stop/reap.
	return p, nil
}

type stdioProcess struct {
	key             mcpruntime.InstanceKey
	command         *exec.Cmd
	cancel          context.CancelFunc
	input           io.WriteCloser
	output          io.ReadCloser
	exited, stopped chan struct{}
	mu              sync.Mutex
	closing         bool
	instance        *clientInstance
	discoveryDone   chan struct{}
	stopOnce        sync.Once
}

func (p *stdioProcess) Done() <-chan struct{} { return p.exited }

func (p *stdioProcess) Discover(ctx context.Context) ([]mcpruntime.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closing || p.discoveryDone != nil {
		p.mu.Unlock()
		return nil, &mcpruntime.LaunchError{Reason: mcpruntime.FailureUnavailable}
	}
	p.discoveryDone = make(chan struct{})
	discoveryDone := p.discoveryDone
	p.mu.Unlock()
	type result struct {
		tools []mcpruntime.Tool
		err   error
	}
	done := make(chan result)
	go func() {
		defer close(discoveryDone)
		client := mcp.NewClient(&mcp.Implementation{Name: "balda-runtime-catalog", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.IOTransport{Reader: p.output, Writer: p.input}, nil)
		var instance *clientInstance
		if err == nil {
			instance = &clientInstance{session: session}
			err = instance.discover(ctx, p.key)
		}
		p.mu.Lock()
		closing := p.closing || ctx.Err() != nil
		if err == nil && !closing {
			p.instance = instance
		}
		p.mu.Unlock()
		var tools []mcpruntime.Tool
		if err == nil && !closing {
			tools = instance.Tools()
		} else if session != nil {
			_ = session.Close()
		}
		if err != nil || closing {
			err = launchFailure(ctx, nil)
		}
		select {
		case done <- result{tools: tools, err: err}:
		case <-ctx.Done():
		}
	}()
	select {
	case value := <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return value.tools, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *stdioProcess) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.closing = true
		instance, discoveryDone := p.instance, p.discoveryDone
		p.mu.Unlock()
		p.cancel()
		_ = p.input.Close()
		_ = p.output.Close()
		go func() {
			if instance != nil {
				_ = instance.session.Close()
			}
			<-p.exited
			if discoveryDone != nil {
				<-discoveryDone
			}
			close(p.stopped)
		}()
	})
	select {
	case <-p.stopped:
		return nil
	case <-ctx.Done():
		_ = p.command.Process.Kill()
		return ctx.Err()
	}
}

var _ mcpruntime.ProcessLauncher = (*ClientLauncher)(nil)
var _ mcpruntime.Process = (*stdioProcess)(nil)
