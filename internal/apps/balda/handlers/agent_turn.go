package handlers

import (
	"context"
	"fmt"
	"strings"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

const defaultGoalMaxIterations = 25

func normalizeGoalMaxIterations(v int) int {
	if v <= 0 {
		return defaultGoalMaxIterations
	}
	return v
}

func runAgentTurn(
	ctx context.Context,
	r *runner.Runner,
	userID string,
	goalSessionID string,
	prompt string,
) (string, error) {
	return runAgentTurnWithProgress(ctx, r, userID, goalSessionID, prompt, nil)
}

func runAgentTurnWithProgress(
	ctx context.Context,
	r *runner.Runner,
	userID string,
	goalSessionID string,
	prompt string,
	onProgress func(string),
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
	for ev, err := range r.Run(ctx, userID, goalSessionID, userContent, adkagent.RunConfig{}) {
		if err != nil {
			return "", fmt.Errorf("run agent turn: %w", err)
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
				if onProgress != nil && !ev.IsFinalResponse() {
					onProgress(part.Text)
				}
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

func runGoalIteration(
	ctx context.Context,
	r *runner.Runner,
	userID string,
	goalSessionID string,
	prompt string,
) (string, error) {
	return runAgentTurn(ctx, r, userID, goalSessionID, prompt)
}
