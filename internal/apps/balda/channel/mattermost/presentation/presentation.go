// Package presentation renders transport-neutral structured service messages
// as Mattermost Markdown. Rendering stays inside the Mattermost subtree by
// contract (docs/architecture/transport-presentation-boundary.md).
package presentation

import (
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

// RenderLocator renders a validated locator response as Mattermost Markdown.
func RenderLocator(body locatorfmt.Response) string {
	return fmt.Sprintf(
		"📍 **Balda Locator** • **Transport:** `%s` • **Locator:** `%s`\n\n**Scheduler / webhook configuration**\n```yaml\ntarget: locator\nkey: %s\n```",
		strings.TrimSpace(body.Transport),
		strings.TrimSpace(body.Locator),
		strings.TrimSpace(body.Locator),
	)
}

// RenderGoalProgress renders a GoalProgress event into Mattermost Markdown.
func RenderGoalProgress(p goalkeepercmd.GoalProgress) string {
	switch p.Kind {
	case goalkeepercmd.GoalProgressKindStarted:
		return fmt.Sprintf(
			"**Goal run started**\n\n- **Max iterations:** %d\n- **Objective:** %s",
			p.MaxIterations,
			strings.TrimSpace(p.Objective),
		)
	case goalkeepercmd.GoalProgressKindStep:
		header := fmt.Sprintf(
			"**Goal iteration %d/%d:** %s %s.",
			p.Iteration, p.MaxIterations, strings.TrimSpace(p.Step), strings.TrimSpace(p.Action),
		)
		if body := strings.TrimSpace(p.Body); body != "" {
			return header + "\n\n" + body
		}
		return header
	case goalkeepercmd.GoalProgressKindStatus:
		return fmt.Sprintf("**%s**", strings.TrimSpace(p.Text))
	default:
		return strings.TrimSpace(p.Text)
	}
}

// RenderGoalOutcome renders a GoalOutcome into Mattermost Markdown.
func RenderGoalOutcome(o goalkeepercmd.GoalOutcome) string {
	var parts []string
	if o.GoalReached {
		parts = append(parts, "**Result:** Goal completed.")
	} else {
		parts = append(parts, "**Result:** Goal not completed.")
	}
	if o.GoalReached && strings.TrimSpace(o.ExportStatus) != "" {
		switch strings.TrimSpace(o.ExportStatus) {
		case goalkeepercmd.GoalExportStatusExported:
		case goalkeepercmd.GoalExportStatusNotExported:
		case goalkeepercmd.GoalExportStatusFailed:
			errText := firstNonEmpty(o.ExportError, o.ExportReason, "unknown error")
			parts = append(parts, "**Export:** failed: "+errText)
		default:
			parts = append(parts, "**Export:** "+strings.TrimSpace(o.ExportStatus)+".")
		}
	}
	if whatWasDone := strings.TrimSpace(o.WhatWasDone); whatWasDone != "" {
		if o.RoutineSuccess() {
			parts = append(parts, whatWasDone)
		} else {
			parts = append(parts, "**What was done:**\n"+whatWasDone)
		}
	}
	if o.ShouldRenderValidation() {
		parts = append(parts, "**Validation:**\n"+strings.TrimSpace(o.Validation))
	}
	if o.ShouldRenderVerified() {
		parts = append(parts, "**Verified:** "+o.ResolvedVerified())
	}
	if o.ShouldRenderNotVerified() {
		parts = append(parts, "**Not verified:** "+o.ResolvedNotVerified())
	}
	if o.ShouldRenderNextAction() {
		parts = append(parts, "**Next action:** "+o.ResolvedNextAction())
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
