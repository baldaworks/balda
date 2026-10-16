// Package proxyruntime supplies provider-neutral ACP process forwarding.
package proxyruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

// Behavior owns the provider-specific launch mutation and wire checks.
type Behavior interface {
	Prepare(ctx context.Context, binding hookprovider.Binding, path string, argv []string, environment map[string]string) ([]string, error)
	RewriteRequest(line []byte) ([]byte, error)
	CheckResponse(line []byte) error
}

// Proxy forwards ACP traffic while applying provider behavior.
func Proxy(parent context.Context, binding hookprovider.Binding, path string, argv []string, input io.Reader, output, stderr io.Writer, behavior Behavior) error {
	if len(argv) == 0 {
		return hookcmd.ErrUnavailable
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	environment := make(map[string]string)
	for _, value := range os.Environ() {
		if key, value, ok := strings.Cut(value, "="); ok {
			environment[key] = value
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	environment[BindingEnv], environment[ExecutableEnv] = path, executable
	argv, err = behavior.Prepare(ctx, binding, path, append([]string(nil), argv...), environment)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = make([]string, 0, len(environment))
	for key, value := range maps.All(environment) {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stderr, command.WaitDelay = stderr, time.Second
	hookprovider.ConfigureProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return hookcmd.ErrUnavailable
	}
	releaseProcess, err := binding.RegisterProcess(ctx, command.Process.Pid)
	if err != nil {
		cancel()
		_ = hookprovider.WaitProcess(command)
		return err
	}
	defer releaseProcess()
	var handshakeMu sync.Mutex
	var initializeID string
	sessionRequests := make(map[string]bool)
	inputError := make(chan error, 1)
	go func() {
		scanner := Scanner(input)
		for scanner.Scan() {
			line := scanner.Bytes()
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(line, &request) == nil && request.Method == "initialize" {
				handshakeMu.Lock()
				initializeID = string(request.ID)
				handshakeMu.Unlock()
			}
			switch request.Method {
			case "session/new", "session/load", "session/resume":
				handshakeMu.Lock()
				sessionRequests[string(request.ID)] = true
				handshakeMu.Unlock()
			}
			line, err = behavior.RewriteRequest(line)
			if err != nil {
				inputError <- err
				cancel()
				return
			}
			if _, err := stdin.Write(append(line, '\n')); err != nil {
				inputError <- err
				cancel()
				return
			}
		}
		_ = stdin.Close()
		inputError <- scanner.Err()
		cancel()
	}()
	scanner := Scanner(stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		var response struct {
			ID     json.RawMessage            `json:"id"`
			Error  json.RawMessage            `json:"error"`
			Result map[string]json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(line, &response)
		handshakeMu.Lock()
		mustVerify := initializeID != "" && initializeID == string(response.ID)
		mustBeReady := sessionRequests[string(response.ID)]
		delete(sessionRequests, string(response.ID))
		handshakeMu.Unlock()
		if mustBeReady && len(response.Error) == 0 {
			if err := binding.WaitReady(ctx); err != nil {
				cancel()
				_ = hookprovider.WaitProcess(command)
				return err
			}
		}
		if mustVerify && len(response.Error) == 0 && len(response.Result["agentInfo"]) == 0 {
			cancel()
			_ = hookprovider.WaitProcess(command)
			return hookcmd.ErrUnsupported
		}
		if err := behavior.CheckResponse(line); err != nil {
			cancel()
			_ = hookprovider.WaitProcess(command)
			return err
		}
		if _, err := output.Write(append(line, '\n')); err != nil {
			cancel()
			_ = hookprovider.WaitProcess(command)
			return err
		}
	}
	interrupted := ctx.Err() != nil
	cancel()
	waitErr := hookprovider.WaitProcess(command)
	inputEnded := false
	select {
	case err := <-inputError:
		if err != nil {
			return hookcmd.ErrUnavailable
		}
		inputEnded = true
	default:
	}
	if scanner.Err() != nil {
		return hookcmd.ErrUnavailable
	}
	if waitErr != nil && !inputEnded && !interrupted {
		cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if binding.UnregisterProcess(cleanupCtx, command.Process.Pid) != nil {
			return nil
		}
		return hookcmd.ErrUnavailable
	}
	return nil
}

// Scanner creates the bounded newline-delimited ACP scanner.
func Scanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	return scanner
}
