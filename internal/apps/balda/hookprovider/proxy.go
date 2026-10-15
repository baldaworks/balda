package hookprovider

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

//go:embed opencode.mjs
var openCodePlugin []byte

func nativeCommand() string {
	if runtime.GOOS == "windows" {
		return `"%BALDA_HOOK_EXECUTABLE%" agent-hooks dispatch`
	}
	return `"${BALDA_HOOK_EXECUTABLE}" agent-hooks dispatch`
}

func nativeReadyCommand() string { return strings.TrimSuffix(nativeCommand(), "dispatch") + "ready" }

// Claude's JavaScript AbortSignal timer overflows beyond signed 32-bit
// milliseconds. Keep its outer native envelope at the largest safe whole
// second value; the Balda runner applies the live per-command deadline.
const claudeEnvelopeSeconds = 2147483

func hookGroups() map[string]any {
	group := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": nativeCommand(), "timeout": claudeEnvelopeSeconds}}}}
	return map[string]any{"PreToolUse": group, "PostToolUse": group, "SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": nativeReadyCommand(), "timeout": 10}}}}}
}

func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, hookcmd.ErrInvalid
	}
	return values, nil
}

func encode(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func injectClaude(line []byte) ([]byte, error) {
	message, err := object(line)
	if err != nil {
		return nil, err
	}
	var method string
	_ = json.Unmarshal(message["method"], &method)
	switch method {
	case "session/new", "session/load", "session/resume":
	default:
		return line, nil
	}
	params, err := object(message["params"])
	if err != nil {
		return nil, err
	}
	meta, err := object(params["_meta"])
	if err != nil {
		return nil, err
	}
	claude, err := object(meta["claudeCode"])
	if err != nil {
		return nil, err
	}
	options, err := object(claude["options"])
	if err != nil {
		return nil, err
	}
	extraArgs, err := object(options["extraArgs"])
	if err != nil {
		return nil, err
	}
	settingsRaw := options["settings"]
	if len(extraArgs["settings"]) != 0 {
		settingsRaw = extraArgs["settings"]
	}
	settings, err := nativeSettings(settingsRaw, params["cwd"])
	if err != nil {
		return nil, err
	}
	var disabled bool
	_ = json.Unmarshal(settings["disableAllHooks"], &disabled)
	if disabled {
		return nil, hookcmd.ErrUnsupported
	}
	hooks, err := object(settings["hooks"])
	if err != nil {
		return nil, err
	}
	for name, group := range hookGroups() {
		var existing []json.RawMessage
		if len(hooks[name]) != 0 && json.Unmarshal(hooks[name], &existing) != nil {
			return nil, hookcmd.ErrInvalid
		}
		var extra []json.RawMessage
		_ = json.Unmarshal(encode(group), &extra)
		hooks[name] = encode(append(existing, extra...))
	}
	settings["hooks"] = encode(hooks)
	// The published ACP 0.16.2 SDK has no Options.settings field. extraArgs
	// forwards the documented native --settings JSON flag on that SDK too.
	extraArgs["settings"] = encode(string(encode(settings)))
	options["extraArgs"] = encode(extraArgs)
	delete(options, "settings")
	claude["options"] = encode(options)
	meta["claudeCode"] = encode(claude)
	params["_meta"] = encode(meta)
	message["params"] = encode(params)
	return json.Marshal(message)
}

func nativeSettings(raw, cwdRaw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '"' {
		return object(raw)
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, hookcmd.ErrInvalid
	}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		return object([]byte(value))
	}
	var cwd string
	_ = json.Unmarshal(cwdRaw, &cwd)
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, hookcmd.ErrUnsupported
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, hookcmd.ErrUnsupported
	}
	return object(data)
}

func openCodeEnvironment(path string, environment map[string]string) error {
	config, err := object([]byte(environment["OPENCODE_CONFIG_CONTENT"]))
	if err != nil {
		return hookcmd.ErrUnsupported
	}
	var disabled bool
	_ = json.Unmarshal(config["disable_plugins"], &disabled)
	if disabled {
		return hookcmd.ErrUnsupported
	}
	var plugins []json.RawMessage
	if len(config["plugin"]) != 0 && json.Unmarshal(config["plugin"], &plugins) != nil {
		return hookcmd.ErrInvalid
	}
	pluginPath := filepath.Join(filepath.Dir(path), "opencode.mjs")
	if err := os.WriteFile(pluginPath, openCodePlugin, 0600); err != nil {
		return err
	}
	urlPath := filepath.ToSlash(pluginPath)
	if filepath.VolumeName(pluginPath) != "" {
		urlPath = "/" + urlPath
	}
	pluginURL := (&url.URL{Scheme: "file", Path: urlPath}).String()
	config["plugin"] = encode(append(plugins, encode(pluginURL)))
	environment["OPENCODE_CONFIG_CONTENT"] = string(encode(config))
	return nil
}

// Proxy forwards ACP unchanged except for session-owned native hook setup.
func Proxy(parent context.Context, path string, argv []string, input io.Reader, output, stderr io.Writer) error {
	binding, err := readBinding(path)
	if err != nil || len(argv) == 0 {
		return hookcmd.ErrUnavailable
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	environment := make(map[string]string)
	for _, value := range os.Environ() {
		key, value, ok := strings.Cut(value, "=")
		if ok {
			environment[key] = value
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	environment[bindingEnv], environment[executableEnv] = path, executable
	argv = append([]string(nil), argv...)
	switch binding.Kind {
	case kindCodex:
		argv, err = prepareCodex(ctx, argv, func(pid int) (func(), error) { return registerNativeProcess(ctx, binding, pid) })
	case kindOpenCode:
		err = openCodeEnvironment(path, environment)
	}
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = make([]string, 0, len(environment))
	for key, value := range maps.All(environment) {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stderr = stderr
	command.WaitDelay = time.Second
	configureProxyProcess(command)
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
	releaseProcess, err := registerNativeProcess(ctx, binding, command.Process.Pid)
	if err != nil {
		cancel()
		_ = waitOwnedProcess(command)
		return err
	}
	defer releaseProcess()
	var handshakeMu sync.Mutex
	var initializeID string
	sessionRequests := make(map[string]bool)
	inputError := make(chan error, 1)
	go func() {
		scanner := protocolScanner(input)
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
			if binding.Kind == kindClaude {
				injected, injectErr := injectClaude(line)
				if injectErr != nil {
					inputError <- injectErr
					cancel()
					return
				}
				line = injected
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
	scanner := protocolScanner(stdout)
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
			if err := waitReady(ctx, binding); err != nil {
				cancel()
				_ = waitOwnedProcess(command)
				return err
			}
		}
		if mustVerify && len(response.Error) == 0 && len(response.Result["agentInfo"]) == 0 {
			cancel()
			_ = waitOwnedProcess(command)
			return hookcmd.ErrUnsupported
		}
		if err := checkNativeVersion(line, binding.Kind); err != nil {
			cancel()
			_ = waitOwnedProcess(command)
			return err
		}
		if _, err := output.Write(append(line, '\n')); err != nil {
			cancel()
			_ = waitOwnedProcess(command)
			return err
		}
	}
	interrupted := ctx.Err() != nil
	cancel()
	waitErr := waitOwnedProcess(command)
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
		// Session release revokes the capability before terminating its group.
		// Treat that deliberate termination as a clean proxy shutdown.
		cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if sendProcessMutation(cleanupCtx, binding, processRequest{Operation: "detach", ProxyPID: os.Getpid(), ChildPID: command.Process.Pid}) != nil {
			return nil
		}
		return hookcmd.ErrUnavailable
	}
	return nil
}

func protocolScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	return scanner
}

func checkNativeVersion(line []byte, kind string) error {
	message, err := object(line)
	if err != nil {
		return hookcmd.ErrInvalid
	}
	result, err := object(message["result"])
	if err != nil || len(result["agentInfo"]) == 0 {
		return nil
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(result["agentInfo"], &info) != nil {
		return hookcmd.ErrUnsupported
	}
	minimum := []int{0, 16, 2}
	switch kind {
	case kindOpenCode:
		minimum = []int{1, 19, 0}
	case kindCodex:
		minimum = []int{1, 10, 1}
	}
	version := strings.TrimPrefix(strings.Split(info.Version, "-")[0], "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return hookcmd.ErrUnsupported
	}
	for i, value := range parts {
		actual, err := strconv.Atoi(value)
		if err != nil {
			return hookcmd.ErrUnsupported
		}
		if actual > minimum[i] {
			return nil
		}
		if actual < minimum[i] {
			return hookcmd.ErrUnsupported
		}
	}
	return nil
}
