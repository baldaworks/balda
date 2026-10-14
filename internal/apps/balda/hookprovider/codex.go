package hookprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

type codexHook struct {
	Key     string `json:"key"`
	Hash    string `json:"currentHash"`
	Source  string `json:"source"`
	Command string `json:"command"`
	Event   string `json:"eventName"`
	Enabled bool   `json:"enabled"`
	Trust   string `json:"trustStatus"`
}

func codexHookArguments() []string {
	group := `[{hooks=[{type="command",command=` + strconv.Quote(nativeCommand()) + `,timeout=125}]}]`
	ready := `[{hooks=[{type="command",command=` + strconv.Quote(nativeReadyCommand()) + `,timeout=10}]}]`
	return []string{"-c", "hooks.PreToolUse=" + group, "-c", "hooks.PostToolUse=" + group, "-c", "hooks.SessionStart=" + ready}
}

func prepareCodex(ctx context.Context, argv []string, registrars ...func(int) (func(), error)) ([]string, error) {
	var registrar func(int) (func(), error)
	if len(registrars) != 0 {
		registrar = registrars[0]
	}
	var existing []string
	for i := 1; i < len(argv); i++ {
		value := argv[i]
		if value == "--codex-args" {
			i++
			if i == len(argv) {
				return nil, hookcmd.ErrInvalid
			}
			existing = append(existing, argv[i])
		} else if strings.HasPrefix(value, "--codex-args=") {
			existing = append(existing, strings.TrimPrefix(value, "--codex-args="))
		}
	}
	for _, value := range existing {
		if strings.HasPrefix(value, "hooks.") || strings.HasPrefix(value, "features.hooks=") {
			return nil, hookcmd.ErrUnsupported
		}
	}
	arguments := codexHookArguments()
	hooks, err := probeCodex(ctx, append(append([]string(nil), existing...), arguments...), registrar)
	if err != nil {
		return nil, err
	}
	var states []string
	for _, hook := range hooks {
		if hook.Source != "sessionFlags" || hook.Command != nativeCommand() && hook.Command != nativeReadyCommand() {
			continue
		}
		if hook.Key == "" || hook.Hash == "" {
			return nil, hookcmd.ErrUnsupported
		}
		states = append(states, strconv.Quote(hook.Key)+"={enabled=true,trusted_hash="+strconv.Quote(hook.Hash)+"}")
	}
	if len(states) != 3 {
		return nil, hookcmd.ErrUnsupported
	}
	arguments = append(arguments, "-c", "hooks.state={"+strings.Join(states, ",")+"}")
	verified, err := probeCodex(ctx, append(append([]string(nil), existing...), arguments...), registrar)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, hook := range verified {
		if hook.Source == "sessionFlags" && (hook.Command == nativeCommand() || hook.Command == nativeReadyCommand()) {
			if !hook.Enabled || hook.Trust != "trusted" {
				return nil, hookcmd.ErrUnsupported
			}
			count++
		}
	}
	if count != 3 {
		return nil, hookcmd.ErrUnsupported
	}
	command := append([]string(nil), argv...)
	for _, argument := range arguments {
		command = append(command, "--codex-args="+argument)
	}
	return command, nil
}

func probeCodex(parent context.Context, arguments []string, registrar func(int) (func(), error)) ([]codexHook, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	executable, err := exec.LookPath(kindCodex)
	if err != nil {
		return nil, hookcmd.ErrUnsupported
	}
	command := exec.CommandContext(ctx, executable, append(arguments, "app-server")...)
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	configureProxyProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, hookcmd.ErrUnavailable
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, hookcmd.ErrUnavailable
	}
	if command.Start() != nil {
		return nil, hookcmd.ErrUnsupported
	}
	var releaseProcess func()
	if registrar != nil {
		releaseProcess, err = registrar(command.Process.Pid)
		if err != nil {
			cancel()
			_ = command.Wait()
			return nil, err
		}
	}
	defer func() {
		cancel()
		_ = command.Wait()
		if releaseProcess != nil {
			releaseProcess()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		data := encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := stdin.Write(append(data, '\n')); err != nil {
			return nil, hookcmd.ErrUnsupported
		}
		for scanner.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil {
				return nil, hookcmd.ErrUnsupported
			}
			if response.ID != id {
				continue
			}
			if len(response.Error) != 0 || len(response.Result) == 0 {
				return nil, hookcmd.ErrUnsupported
			}
			return response.Result, nil
		}
		return nil, hookcmd.ErrUnsupported
	}
	if _, err := rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "balda-native-hook-adapter", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"initialized","params":{}}`); err != nil {
		return nil, hookcmd.ErrUnsupported
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, hookcmd.ErrUnavailable
	}
	data, err := rpc(2, "hooks/list", map[string]any{"cwds": []string{cwd}})
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			Hooks []codexHook `json:"hooks"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, hookcmd.ErrUnsupported
	}
	var hooks []codexHook
	for _, entry := range response.Data {
		hooks = append(hooks, entry.Hooks...)
	}
	return hooks, nil
}
