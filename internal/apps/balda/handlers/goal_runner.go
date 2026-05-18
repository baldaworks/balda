package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

const defaultGoalMaxIterations = 25

type goalCommandRunner interface {
	Start(ctx context.Context, locator baldasession.SessionLocator, objective string, transportUserID string) (bool, error)
	Cancel(locator baldasession.SessionLocator) bool
}

type goalRunnerParams struct {
	fx.In

	LC             fx.Lifecycle
	SessionManager *baldasession.Manager
	Channel        *baldatelegram.Adapter
	Logger         zerolog.Logger
	MaxIterations  int `name:"balda_goal_max_iterations"`
}

// GoalRunner executes /goal loops per session with cancellation support.
type GoalRunner struct {
	sessionManager *baldasession.Manager
	channel        *baldatelegram.Adapter
	logger         zerolog.Logger
	maxIterations  int

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewGoalRunner(params goalRunnerParams) *GoalRunner {
	g := &GoalRunner{
		sessionManager: params.SessionManager,
		channel:        params.Channel,
		logger:         params.Logger.With().Str("component", "balda.goal_runner").Logger(),
		maxIterations:  normalizeGoalMaxIterations(params.MaxIterations),
		running:        make(map[string]context.CancelFunc),
	}
	params.LC.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			g.stopAll()
			return nil
		},
	})
	return g
}

func normalizeGoalMaxIterations(v int) int {
	if v <= 0 {
		return defaultGoalMaxIterations
	}
	return v
}

func (g *GoalRunner) Start(
	ctx context.Context,
	locator baldasession.SessionLocator,
	objective string,
	transportUserID string,
) (bool, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	goal := strings.TrimSpace(objective)
	if sessionID == "" {
		return false, fmt.Errorf("session id is required")
	}
	if goal == "" {
		return false, fmt.Errorf("goal objective is required")
	}

	g.mu.Lock()
	if _, exists := g.running[sessionID]; exists {
		g.mu.Unlock()
		return false, nil
	}
	g.mu.Unlock()

	ts, err := g.resolveSession(ctx, locator, transportUserID)
	if err != nil {
		return false, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	if _, exists := g.running[sessionID]; exists {
		g.mu.Unlock()
		cancel()
		return false, nil
	}
	g.running[sessionID] = cancel
	g.mu.Unlock()

	go func() {
		defer g.removeRun(sessionID)
		g.runGoalLoop(runCtx, locator, ts, goal)
	}()

	return true, nil
}

func (g *GoalRunner) resolveSession(
	ctx context.Context,
	locator baldasession.SessionLocator,
	transportUserID string,
) (*baldasession.TopicSession, error) {
	ts, err := g.sessionManager.GetSession(locator)
	if err == nil {
		return ts, nil
	}

	return g.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  transportUserID,
	})
}

func (g *GoalRunner) runGoalLoop(
	ctx context.Context,
	locator baldasession.SessionLocator,
	ts *baldasession.TopicSession,
	objective string,
) {
	if ts == nil {
		_ = g.channel.SendPlain(ctx, locator, "Goal run failed: session is unavailable.")
		return
	}

	maxIterations := g.maxIterations
	goalSessionID := strings.TrimSpace(ts.GetAgentSessionID())
	if goalSessionID == "" {
		_ = g.channel.SendPlain(ctx, locator, "Goal run failed: session is unavailable.")
		return
	}
	_ = g.channel.SendPlain(
		ctx,
		locator,
		fmt.Sprintf("Goal run started. Max iterations: %d.\nGoal: %s", maxIterations, objective),
	)

	previousSummary := ""
	for iteration := 1; iteration <= maxIterations; iteration++ {
		if ctx.Err() != nil {
			_ = g.channel.SendPlain(context.Background(), locator, "Goal run canceled.")
			return
		}

		prompt := buildGoalPrompt(objective, previousSummary, iteration, maxIterations)
		reply, err := runGoalIteration(ctx, ts.GetRunner(), ts.GetUserID(), goalSessionID, prompt)
		if err != nil {
			if ctx.Err() != nil {
				_ = g.channel.SendPlain(context.Background(), locator, "Goal run canceled.")
				return
			}
			_ = g.channel.SendPlain(context.Background(), locator, fmt.Sprintf("Goal run failed: %v", err))
			return
		}

		done, message := parseGoalReply(reply)
		if message == "" {
			message = "no update"
		}

		_ = g.channel.SendPlain(
			ctx,
			locator,
			fmt.Sprintf("Goal iteration %d/%d: %s", iteration, maxIterations, message),
		)

		if done {
			_ = g.channel.SendPlain(ctx, locator, "Goal run completed.")
			return
		}
		previousSummary = message
	}

	_ = g.channel.SendPlain(ctx, locator, "Goal run reached max iterations without completion.")
}

func runGoalIteration(
	ctx context.Context,
	r *runner.Runner,
	userID string,
	goalSessionID string,
	prompt string,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil {
		return "", fmt.Errorf("runner is required")
	}
	userContent := genai.NewContentFromText(prompt, genai.RoleUser)

	var out strings.Builder
	sawTurnComplete := false
	for ev, err := range r.Run(ctx, userID, goalSessionID, userContent, agent.RunConfig{}) {
		if err != nil {
			return "", fmt.Errorf("run goal iteration: %w", err)
		}
		if ev == nil {
			continue
		}
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part == nil || part.Thought || part.Text == "" {
					continue
				}
				out.WriteString(part.Text)
			}
		}
		if ev.TurnComplete {
			sawTurnComplete = true
		}
	}
	if !sawTurnComplete {
		return strings.TrimSpace(out.String()), fmt.Errorf("goal iteration ended without completion")
	}
	return strings.TrimSpace(out.String()), nil
}

func buildGoalPrompt(objective, previous string, iteration, maxIterations int) string {
	return fmt.Sprintf(
		`You are Goalkeeper running an iterative goal loop.
Objective: %s
Previous iteration summary: %s
Iteration: %d/%d

Return exactly two lines:
STATUS: done|continue
MESSAGE: <one concise update for the user>

Use STATUS: done only when the objective is fully complete.`,
		strings.TrimSpace(objective),
		strings.TrimSpace(previous),
		iteration,
		maxIterations,
	)
}

func parseGoalReply(reply string) (done bool, message string) {
	text := strings.TrimSpace(reply)
	if text == "" {
		return false, ""
	}

	lines := strings.Split(text, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "STATUS:") {
			value := strings.TrimSpace(line[len("STATUS:"):])
			done = strings.EqualFold(value, "done")
			continue
		}
		if strings.HasPrefix(upper, "MESSAGE:") {
			message = strings.TrimSpace(line[len("MESSAGE:"):])
		}
	}

	if strings.TrimSpace(message) == "" {
		message = text
	}
	return done, strings.TrimSpace(message)
}

func (g *GoalRunner) Cancel(locator baldasession.SessionLocator) bool {
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" {
		return false
	}

	g.mu.Lock()
	cancel := g.running[sessionID]
	g.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (g *GoalRunner) removeRun(sessionID string) {
	g.mu.Lock()
	delete(g.running, sessionID)
	g.mu.Unlock()
}

func (g *GoalRunner) stopAll() {
	g.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(g.running))
	for _, cancel := range g.running {
		cancels = append(cancels, cancel)
	}
	g.running = make(map[string]context.CancelFunc)
	g.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}
