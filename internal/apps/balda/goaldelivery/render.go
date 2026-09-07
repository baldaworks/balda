package goaldelivery

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/redaction"
)

const (
	DefaultNotVerifiedText       = goalkeepercmd.DefaultNotVerifiedText
	DefaultInspectNextAction     = goalkeepercmd.DefaultInspectNextAction
	DefaultExportedNextAction    = goalkeepercmd.DefaultExportedNextAction
	DefaultNotExportedNextAction = goalkeepercmd.DefaultNotExportedNextAction

	GoalExportStatusExported    = goalkeepercmd.GoalExportStatusExported
	GoalExportStatusFailed      = goalkeepercmd.GoalExportStatusFailed
	GoalExportStatusNotExported = goalkeepercmd.GoalExportStatusNotExported
)

type messageStyle string

const (
	messageStylePlain    messageStyle = "plain"
	messageStyleMarkdown messageStyle = "markdown"
)

type messageTemplate string

const (
	templateStarted messageTemplate = "started"
	templateStep    messageTemplate = "step"
	templateStatus  messageTemplate = "status"
)

type messageData struct {
	MaxIterations int
	Iteration     int
	Objective     string
	Step          string
	Action        string
	Body          string
	Text          string
}

var messageTemplates = map[messageStyle]map[messageTemplate]*template.Template{
	messageStylePlain: mustTemplates(messageStylePlain, map[messageTemplate]string{
		templateStarted: "Goal run started. Max iterations: {{.MaxIterations}}.\n\nObjective: {{.Objective}}",
		templateStep:    "Goal iteration {{.Iteration}}/{{.MaxIterations}}: {{.Step}} {{.Action}}.{{if .Body}}\n\n{{.Body}}{{end}}",
		templateStatus:  "{{.Text}}",
	}),
	messageStyleMarkdown: mustTemplates(messageStyleMarkdown, map[messageTemplate]string{
		templateStarted: "**Goal run started**\n\n- **Max iterations:** {{.MaxIterations}}\n- **Objective:** {{.Objective}}",
		templateStep:    "**Goal iteration {{.Iteration}}/{{.MaxIterations}}:** {{.Step}} {{.Action}}.{{if .Body}}\n\n{{.Body}}{{end}}",
		templateStatus:  "**{{.Text}}**",
	}),
}

func mustTemplates(style messageStyle, sources map[messageTemplate]string) map[messageTemplate]*template.Template {
	out := make(map[messageTemplate]*template.Template, len(sources))
	for name, source := range sources {
		out[name] = template.Must(template.New(string(style) + "." + string(name)).Option("missingkey=error").Parse(source))
	}
	return out
}

func RenderStartedMessage(format deliveryfmt.DeliveryFormat, maxIterations int, objective string) string {
	style := messageStyleForFormat(format)
	return renderTemplate(style, templateStarted, messageData{MaxIterations: maxIterations, Objective: strings.TrimSpace(objective)})
}

func RenderStepMessage(format deliveryfmt.DeliveryFormat, iteration int, maxIterations int, step string, action string, body string) string {
	style := messageStyleForFormat(format)
	return renderTemplate(style, templateStep, messageData{MaxIterations: maxIterations, Iteration: iteration, Step: strings.TrimSpace(step), Action: strings.TrimSpace(action), Body: strings.TrimSpace(body)})
}

func RenderStatusMessage(format deliveryfmt.DeliveryFormat, text string) string {
	style := messageStyleForFormat(format)
	return renderTemplate(style, templateStatus, messageData{Text: strings.TrimSpace(text)})
}

func RenderProgress(format deliveryfmt.DeliveryFormat, progress goalkeepercmd.GoalProgress) string {
	switch progress.Kind {
	case goalkeepercmd.GoalProgressKindStarted:
		return RenderStartedMessage(format, progress.MaxIterations, progress.Objective)
	case goalkeepercmd.GoalProgressKindStep:
		return RenderStepMessage(format, progress.Iteration, progress.MaxIterations, progress.Step, progress.Action, progress.Body)
	case goalkeepercmd.GoalProgressKindStatus:
		return RenderStatusMessage(format, progress.Text)
	default:
		return RenderStatusMessage(format, progress.Text)
	}
}

func RenderReviewableOutcome(format deliveryfmt.DeliveryFormat, outcome goalkeepercmd.GoalOutcome) string {
	goalReached := outcome.GoalReached
	exportStatus := strings.TrimSpace(outcome.ExportStatus)
	exportReason := strings.TrimSpace(outcome.ExportReason)
	exportError := strings.TrimSpace(outcome.ExportError)
	whatWasDone := strings.TrimSpace(outcome.WhatWasDone)

	var parts []string
	if goalReached {
		parts = append(parts, outcomeLine(format, "Result", "Goal completed."))
	} else {
		parts = append(parts, outcomeLine(format, "Result", "Goal not completed."))
	}
	if goalReached && exportStatus != "" {
		switch exportStatus {
		case GoalExportStatusExported:
		case GoalExportStatusNotExported:
		case GoalExportStatusFailed:
			parts = append(parts, outcomeLine(format, "Export", "failed: "+firstNonEmpty(exportError, exportReason, "unknown error")))
		default:
			parts = append(parts, outcomeLine(format, "Export", exportStatus+"."))
		}
	}
	if whatWasDone != "" {
		if outcome.RoutineSuccess() {
			parts = append(parts, whatWasDone)
		} else {
			parts = append(parts, outcomeBlock(format, "What was done", whatWasDone))
		}
	}
	if outcome.ShouldRenderValidation() {
		parts = append(parts, outcomeBlock(format, "Validation", strings.TrimSpace(outcome.Validation)))
	}
	if outcome.ShouldRenderVerified() {
		parts = append(parts, outcomeLine(format, "Verified", outcome.ResolvedVerified()))
	}
	if outcome.ShouldRenderNotVerified() {
		parts = append(parts, outcomeLine(format, "Not verified", outcome.ResolvedNotVerified()))
	}
	if outcome.ShouldRenderNextAction() {
		parts = append(parts, outcomeLine(format, "Next action", outcome.ResolvedNextAction()))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func RedactSecrets(raw string) string {
	return redaction.Secrets(raw)
}

func messageStyleForFormat(format deliveryfmt.DeliveryFormat) messageStyle {
	switch deliveryfmt.NormalizeDeliveryFormat(format) {
	case deliveryfmt.DeliveryFormatRichMarkdown, deliveryfmt.DeliveryFormatMrkdwn, deliveryfmt.DeliveryFormatMarkdown:
		return messageStyleMarkdown
	default:
		return messageStylePlain
	}
}

func renderTemplate(style messageStyle, name messageTemplate, data messageData) string {
	templates := messageTemplates[style]
	if templates == nil {
		templates = messageTemplates[messageStylePlain]
	}
	tmpl := templates[name]
	if tmpl == nil {
		tmpl = messageTemplates[messageStylePlain][name]
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

func outcomeLine(format deliveryfmt.DeliveryFormat, label string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if messageStyleForFormat(format) == messageStylePlain {
		return label + ": " + value
	}
	return fmt.Sprintf("**%s:** %s", label, value)
}

func outcomeBlock(format deliveryfmt.DeliveryFormat, label string, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if messageStyleForFormat(format) == messageStylePlain {
		return label + ":\n" + body
	}
	return fmt.Sprintf("**%s:**\n%s", label, body)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
