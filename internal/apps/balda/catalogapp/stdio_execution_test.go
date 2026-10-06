package catalogapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestMain(m *testing.M) {
	if handled, code := mcpfx.RunStdioMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestStdioDiscoveryAndActualProvidersUseHostEnvironmentAndOverlay(t *testing.T) {
	testStdioActualProviders(t, false)
}

func TestStdioActualACPParentTerminationReleasesChildren(t *testing.T) {
	testStdioActualProviders(t, true)
}

func testStdioActualProviders(t *testing.T, forceACP bool) {
	t.Helper()
	t.Setenv("BALDA_MCP_CATALOG_STDIO_HOST", "host-value")
	t.Setenv("BALDA_MCP_CATALOG_STDIO_OVERLAY", "parent-value")
	t.Setenv("BALDA_MCP_CATALOG_STDIO_REFERENCE_SOURCE", "referenced-value")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		sources []mcpcmd.Source
	}{
		{"managed", []mcpcmd.Source{mcpcmd.SourceManaged, mcpcmd.SourceManaged}},
		{"config", []mcpcmd.Source{mcpcmd.SourceConfig, mcpcmd.SourceConfig}},
		{"mixed", []mcpcmd.Source{mcpcmd.SourceConfig, mcpcmd.SourceManaged}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise real store mutations and both provider transports without
			// coupling this process-launch test to platform database file URIs.
			p, original, _, mutation, credentials := hybridCatalogFixtureWithDatabase(t, ":memory:")
			configured := make(map[string]agentconfig.MCPServerConfig)
			var directories []string
			names := []string{"echo", "echo_two"}
			ids := []string{"worker-tools", "worker-tools-two"}
			var configuredIDs []string
			for index, name := range names {
				directory := t.TempDir()
				directories = append(directories, directory)
				config := agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeStdio, Cmd: []string{executable}, Args: append([]string{"-test.run=^TestMCPCatalogStdioChild$", "--"}, stdioCatalogLiteralArgs()...), WorkingDir: directory, Env: map[string]string{"BALDA_MCP_CATALOG_STDIO_CHILD": "1", "BALDA_MCP_CATALOG_STDIO_OVERLAY": "child-value", "BALDA_MCP_CATALOG_STDIO_DIRECTORY": directory, "BALDA_MCP_CATALOG_STDIO_TOOL": name, "BALDA_MCP_CATALOG_STDIO_REFERENCE": "referenced-value"}}
				if tc.sources[index] == mcpcmd.SourceConfig {
					configured[ids[index]] = config
					configuredIDs = append(configuredIDs, ids[index])
					continue
				}
				next := mutation
				if index == 1 {
					next.Connection.ID, next.Connection.PublicID, next.Connection.CurrentRevisionID = "worker-two", ids[index], "revision-two"
					next.Audit.ID, next.Audit.TargetID = "create-mcp-two", next.Connection.ID
				}
				edits := make(map[string]mcpcmd.ValueEdit)
				for key, value := range config.Env {
					edits[key] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueLiteral, Value: value}
				}
				overlay := edits["BALDA_MCP_CATALOG_STDIO_OVERLAY"]
				overlay.Kind = mcpcmd.ValueProtected
				edits["BALDA_MCP_CATALOG_STDIO_OVERLAY"] = overlay
				edits["BALDA_MCP_CATALOG_STDIO_REFERENCE"] = mcpcmd.ValueEdit{Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: "BALDA_MCP_CATALOG_STDIO_REFERENCE_SOURCE"}
				revision, err := credentials.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: next.Connection.ID, ID: next.Connection.CurrentRevisionID, CreatedAt: next.Connection.UpdatedAt, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: executable, Args: config.Args, Directory: directory, Targets: mcpcmd.Targets{Providers: []string{"alpha"}}}}, mcpcmd.ValueEdits{Env: edits})
				if err != nil {
					t.Fatal(err)
				}
				next.Revision = &revision
				if err := p.MCP().SaveMCPConnection(t.Context(), next); err != nil {
					t.Fatal(err)
				}
			}
			var retained []mcpcmd.Revision
			if len(configuredIDs) > 0 {
				// A prior remote OAuth capture must not alter the ordinary
				// current stdio launch or either actual provider's selection.
				remote := make(map[string]agentconfig.MCPServerConfig)
				for _, id := range configuredIDs {
					remote[id] = agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: "https://worker.example/" + id}
				}
				probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), nil)
				if err != nil {
					t.Fatal(err)
				}
				definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(remote, map[string]agentconfig.Config{"alpha": {MCPServers: configuredIDs}}, "alpha", nil), original, probe)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range configuredIDs {
					capture, err := definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:" + id, Authority: mutation.Authority})
					if err != nil {
						t.Fatal(err)
					}
					retained = append(retained, capture)
				}
			}
			registry := mcpregistry.New(nil)
			processDirectory := t.TempDir()
			t.Setenv("BALDA_MCP_CATALOG_PROCESS_DIRECTORY", processDirectory)
			catalog, err := NewRuntime(original.stateDir, "", "", p, nil, configured, registry, commandcmd.NewRegistry(), credentials, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.MCP().Shutdown(t.Context()) }()
			if len(configuredIDs) > 0 {
				if err := catalog.configureProviderMCP(map[string]agentconfig.Config{"alpha": {MCPServers: configuredIDs}, "beta": {}}, "alpha", nil); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := catalog.PreparePluginCandidate(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.PublishCandidate(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			if health := catalog.MCP().Health(); len(health) != 2 || health[0].ToolCount != 1 || health[1].ToolCount != 1 {
				t.Fatal("stdio discovery lost host environment or protected overlay")
			}
			for _, capture := range retained {
				connection, found, err := p.MCP().GetMCPConnection(t.Context(), capture.ConnectionID)
				if err != nil || !found {
					t.Fatal("retained remote connection missing")
				}
				status, _, err := catalog.MCPHealth(t.Context(), connection)
				if err != nil || status != mcpcmd.StatusReady {
					t.Fatalf("current stdio readiness = %s/%v, want ready", status, err)
				}
				stored, found, err := p.MCP().GetMCPRevision(t.Context(), capture.ConnectionID, capture.ID)
				if err != nil || !found || stored.Definition.URL != capture.Definition.URL {
					t.Fatal("stdio transition changed its retained remote capture")
				}
			}
			for _, descriptor := range snapshot.MCPServers {
				projected, found := registry.Get(descriptorRegistryID(descriptor))
				if !found || projected.Type != agentconfig.MCPServerTypeStdio {
					t.Fatal("stdio was not projected directly")
				}
			}
			observe := catalogProcessObserver(t, directories, processDirectory, forceACP)
			if !forceACP {
				testHostedPoolInvocationWithTools(t, catalog, registry, snapshot.ID, configuredIDs, nil, names, observe)
			}
			testACPInvocationWithServers(t, catalog, registry, snapshot.ID, configuredIDs, nil, observe, forceACP)
			for _, directory := range directories {
				calls, err := os.ReadFile(filepath.Join(directory, "invocations.txt"))
				if err != nil || (!forceACP && !strings.Contains(string(calls), "actual hosted tool")) || !strings.Contains(string(calls), "actual ACP tool") {
					t.Fatalf("actual providers must invoke each independent server: %s: %v", calls, err)
				}
			}
		})
	}
}

func TestMCPCatalogStdioChild(t *testing.T) {
	if os.Getenv("BALDA_MCP_CATALOG_STDIO_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	recordCatalogProcess(t, os.Getenv("BALDA_MCP_CATALOG_STDIO_DIRECTORY"), "MCP")
	server := mcp.NewServer(&mcp.Implementation{Name: "catalog-stdio", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: os.Getenv("BALDA_MCP_CATALOG_STDIO_TOOL")}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		if os.Getenv("BALDA_MCP_CATALOG_STDIO_HOST") != "host-value" || os.Getenv("BALDA_MCP_CATALOG_STDIO_OVERLAY") != "child-value" || os.Getenv("BALDA_MCP_CATALOG_STDIO_REFERENCE") != "referenced-value" || !slices.Equal(os.Args[3:], stdioCatalogLiteralArgs()) {
			return nil, nil, fmt.Errorf("stdio execution environment differs from discovery")
		}
		directory, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		actual, err := os.Stat(directory)
		if err != nil {
			return nil, nil, err
		}
		expected, err := os.Stat(os.Getenv("BALDA_MCP_CATALOG_STDIO_DIRECTORY"))
		if err != nil || !os.SameFile(actual, expected) {
			return nil, nil, fmt.Errorf("stdio execution lost its configured working directory")
		}
		calls, err := os.OpenFile("invocations.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, nil, err
		}
		_, err = fmt.Fprintln(calls, args.Text)
		closeErr := calls.Close()
		if err != nil || closeErr != nil {
			return nil, nil, fmt.Errorf("record stdio invocation")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func stdioCatalogLiteralArgs() []string {
	return []string{"a b", "$HOME", "$(false)", `back\slash`, `quote"here`, "юникод"}
}

// The observer runs on each side of Stop, before caller cancellation can hide
// an ownership leak. Discovery processes are retained by a different owner.
type providerLifecycleObserver func(string) func(bool)

type catalogProcess struct {
	PID    int
	Parent int
}

func recordCatalogProcess(t *testing.T, directory, kind string) {
	t.Helper()
	if directory == "" {
		return
	}
	process := catalogProcess{PID: os.Getpid(), Parent: os.Getppid()}
	data, err := json.Marshal(process)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%s-process-%d.json", kind, process.PID)), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func catalogProcessObserver(t *testing.T, serverDirectories []string, processDirectory string, forceACP bool) providerLifecycleObserver {
	t.Helper()
	directories := append(slices.Clone(serverDirectories), processDirectory)
	return func(provider string) func(bool) {
		before := readCatalogProcesses(t, directories)
		invocations := make([]int, len(serverDirectories))
		for index, directory := range serverDirectories {
			calls, err := os.ReadFile(filepath.Join(directory, "invocations.txt"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			invocations[index] = len(calls)
		}
		return func(stopped bool) {
			if !stopped && !strings.HasSuffix(provider, "pool-beta") {
				want := "actual hosted tool"
				if strings.HasPrefix(provider, "ACP/") {
					want = "actual ACP tool"
				}
				for index, directory := range serverDirectories {
					calls, err := os.ReadFile(filepath.Join(directory, "invocations.txt"))
					if err != nil || len(calls) < invocations[index] || !strings.Contains(string(calls[invocations[index]:]), want) {
						t.Errorf("%s did not invoke server %s in its new session: %v", provider, directory, err)
					}
				}
			}
			current := readCatalogProcesses(t, directories)
			var pids, children, owners []int
			var servers, providers int
			for path, process := range current {
				if _, found := before[path]; found {
					continue
				}
				pids = append(pids, process.PID)
				if strings.HasPrefix(filepath.Base(path), "MCP-") {
					children = append(children, process.PID)
					servers++
					if runtime.GOOS == "windows" {
						pids = append(pids, process.Parent)
						children = append(children, process.Parent)
					}
				} else {
					owners = append(owners, process.PID)
					providers++
				}
			}
			wantServers := len(serverDirectories)
			if strings.HasSuffix(provider, "pool-beta") {
				wantServers = 0
			}
			wantProviders := 0
			if strings.HasPrefix(provider, "ACP/") {
				wantProviders = 1
			}
			if servers != wantServers || providers != wantProviders {
				t.Errorf("%s process observations: MCP=%d ACP=%d, want MCP=%d ACP=%d", provider, servers, providers, wantServers, wantProviders)
			}
			for _, pid := range pids {
				if !stopped {
					alive, err := catalogProcessAlive(pid)
					if err != nil || !alive {
						t.Errorf("%s process %d must be live before owner shutdown: alive=%t error=%v", provider, pid, alive, err)
					}
				}
			}
			if stopped {
				waitCatalogProcessExit(t, provider+" owner Stop", pids)
				return
			}
			if forceACP {
				for _, pid := range owners {
					process, err := os.FindProcess(pid)
					if err != nil {
						t.Fatal(err)
					}
					if err := process.Kill(); err != nil {
						t.Fatal(err)
					}
					_ = process.Release()
				}
				// Stop has not run yet: its cleanup cannot disguise an orphan
				// caused by the intentional death of the real ACP parent.
				waitCatalogProcessExit(t, provider+" forced ACP parent death before Stop", children)
			}
		}
	}
}

func waitCatalogProcessExit(t *testing.T, operation string, pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for {
			alive, err := catalogProcessAlive(pid)
			if err != nil {
				t.Errorf("%s process %d observation: %v", operation, pid, err)
				break
			}
			if !alive {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("%s process %d remains alive", operation, pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func readCatalogProcesses(t *testing.T, directories []string) map[string]catalogProcess {
	t.Helper()
	processes := make(map[string]catalogProcess)
	for _, directory := range directories {
		paths, err := filepath.Glob(filepath.Join(directory, "*-process-*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var process catalogProcess
			if err := json.Unmarshal(data, &process); err != nil || process.PID <= 0 || process.Parent <= 0 {
				t.Fatalf("invalid process observation %s: %v", path, err)
			}
			processes[path] = process
		}
	}
	return processes
}
