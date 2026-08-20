package sessionmemoryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/sessionmemory"
	portableapp "github.com/baldaworks/balda/sessionmemory/app"
	"github.com/normahq/runtime/v2/structuredagent"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// NormaInvoker is the Balda-owned StructuredInvoker backed by a dedicated
// Norma/ADK runtime. Its session service is in-memory and is never shared with
// user chat sessions.
type NormaInvoker struct {
	mu         sync.Mutex
	builder    *baldaagent.Builder
	providerID string
	workingDir string
	runtime    *baldaagent.BuiltRuntime
	maxBytes   int
	timeout    time.Duration
	closed     bool
}

// NormaInvokerConfig selects the extraction provider and working dir for an
// isolated memory derivation runtime. The provider ID is resolved by Balda's
// composition root and remains opaque to this adapter.
type NormaInvokerConfig struct {
	Builder    *baldaagent.Builder
	ProviderID string
	WorkingDir string
	MaxBytes   int
	Timeout    time.Duration
}

// NewNormaInvoker builds an isolated runtime with no MCP server IDs.
func NewNormaInvoker(cfg NormaInvokerConfig) (*NormaInvoker, error) {
	if cfg.Builder == nil {
		return nil, fmt.Errorf("memory runtime builder is required")
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = portableapp.DefaultDerivationMaxOutputBytes
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = portableapp.DefaultDerivationTimeout
	}
	if strings.TrimSpace(cfg.ProviderID) == "" {
		return nil, fmt.Errorf("memory runtime provider is required")
	}
	if strings.TrimSpace(cfg.WorkingDir) == "" {
		return nil, fmt.Errorf("memory runtime working directory is required")
	}
	return &NormaInvoker{
		builder:    cfg.Builder,
		providerID: strings.TrimSpace(cfg.ProviderID),
		workingDir: strings.TrimSpace(cfg.WorkingDir),
		maxBytes:   maxBytes,
		timeout:    timeout,
	}, nil
}

func (n *NormaInvoker) Invoke(ctx context.Context, request portableapp.StructuredInvocation) ([]byte, error) {
	if ctx == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "memory invocation context is required", nil)
	}
	invokeCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "memory runtime is closed", nil)
	}
	if n.runtime == nil {
		runtime, err := n.builder.BuildDedicatedRuntime(invokeCtx, n.providerID, n.workingDir, portableapp.DefaultDerivationInstruction)
		if err != nil {
			if errors.Is(invokeCtx.Err(), context.DeadlineExceeded) {
				return nil, sessionmemory.RetryableError(sessionmemory.CodeTimeout, "build isolated memory runtime timed out", nil)
			}
			return nil, sessionmemory.RetryableError(sessionmemory.CodeModelFailure, "build isolated memory runtime", nil)
		}
		n.runtime = runtime
	}
	if n.runtime.Agent == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "memory runtime agent is unavailable", nil)
	}
	if len(request.InputJSON) > sessionmemory.MaxDerivedTurnTextBytes {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "memory invocation input exceeds the limit", nil)
	}
	if len(request.OutputSchema) == 0 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "memory output schema is required", nil)
	}
	wrapped, err := structuredagent.NewAgent(
		n.runtime.Agent,
		structuredagent.WithSystemInstruction(request.Instruction),
		structuredagent.WithOutputSchema(request.OutputSchema),
		structuredagent.WithMaxAccumulatedOutputBytes(n.maxBytes),
		structuredagent.WithOutputValidationRetries(1),
	)
	if err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "build structured memory wrapper", err)
	}
	r, err := runner.New(runner.Config{AppName: n.runtime.AppName, Agent: wrapped, SessionService: n.runtime.SessionSvc})
	if err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "build structured memory runner", err)
	}
	sessionID := memoryProcessorSessionID(request.OperationID, request.Stage)
	userID := "session-memory-processor"
	_ = n.runtime.SessionSvc.Delete(invokeCtx, &adksession.DeleteRequest{AppName: n.runtime.AppName, UserID: userID, SessionID: sessionID})
	if _, err := n.runtime.SessionSvc.Create(invokeCtx, &adksession.CreateRequest{AppName: n.runtime.AppName, UserID: userID, SessionID: sessionID}); err != nil {
		if errors.Is(invokeCtx.Err(), context.DeadlineExceeded) {
			return nil, sessionmemory.RetryableError(sessionmemory.CodeTimeout, "create isolated memory session timed out", nil)
		}
		return nil, sessionmemory.RetryableError(sessionmemory.CodeModelFailure, "create isolated memory session", nil)
	}
	defer func() {
		_ = n.runtime.SessionSvc.Delete(context.Background(), &adksession.DeleteRequest{AppName: n.runtime.AppName, UserID: userID, SessionID: sessionID})
	}()
	envelope, err := json.Marshal(map[string]string{"input": string(request.InputJSON)})
	if err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode memory invocation input", err)
	}
	var output string
	for event, runErr := range r.Run(invokeCtx, userID, sessionID, genai.NewContentFromText(string(envelope), genai.RoleUser), adkagent.RunConfig{}) {
		if runErr != nil {
			if errors.Is(invokeCtx.Err(), context.DeadlineExceeded) {
				return nil, sessionmemory.RetryableError(sessionmemory.CodeTimeout, "run structured memory derivation timed out", nil)
			}
			return nil, sessionmemory.RetryableError(sessionmemory.CodeModelFailure, "run structured memory derivation", nil)
		}
		if event == nil || event.Content == nil {
			continue
		}
		var text strings.Builder
		for _, part := range event.Content.Parts {
			if part != nil && !part.Thought {
				text.WriteString(part.Text)
			}
		}
		if text.Len() > 0 {
			output = text.String()
		}
	}
	if strings.TrimSpace(output) == "" {
		if errors.Is(invokeCtx.Err(), context.DeadlineExceeded) {
			return nil, sessionmemory.RetryableError(sessionmemory.CodeTimeout, "structured memory derivation timed out", nil)
		}
		return nil, sessionmemory.RetryableError(sessionmemory.CodeModelFailure, "structured memory derivation returned no output", nil)
	}
	if len(output) > n.maxBytes {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "structured memory derivation output exceeds the limit", nil)
	}
	return []byte(output), nil
}

func (n *NormaInvoker) Close(context.Context) error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	if n.runtime != nil {
		if closer, ok := n.runtime.Agent.(io.Closer); ok {
			return closer.Close()
		}
	}
	return nil
}

func memoryProcessorSessionID(operationID, stage string) string {
	hash := sha256.Sum256([]byte(operationID + "\x00" + stage))
	return "memory-processor-" + hex.EncodeToString(hash[:])
}

var _ portableapp.StructuredInvoker = (*NormaInvoker)(nil)
