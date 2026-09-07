package goaldelivery

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/redaction"
)

const (
	DefaultNotVerifiedText       = "manual review still required"
	DefaultInspectNextAction     = "Inspect events and decide whether to continue, cancel, or ask a human."
	DefaultExportedNextAction    = "Review the exported result and continue with follow-up work if needed."
	DefaultNotExportedNextAction = "Review the direct working directory changes and commit or follow up manually if needed."

	GoalExportStatusExported    = "exported"
	GoalExportStatusFailed      = "export_failed"
	GoalExportStatusNotExported = "not_exported"
)

type messageStyle string

const (
	messageStylePlain    messageStyle = "plain"
	messageStyleMarkdown messageStyle = "markdown"
	messageStyleHTML     messageStyle = "html"
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
	messageStyleHTML: mustTemplates(messageStyleHTML, map[messageTemplate]string{
		templateStarted: "<b>Goal run started</b>\n\n<b>Max iterations:</b> {{.MaxIterations}}\n\n<b>Objective:</b> {{.Objective}}",
		templateStep:    "<b>Goal iteration {{.Iteration}}/{{.MaxIterations}}:</b> {{.Step}} {{.Action}}.{{if .Body}}\n\n{{.Body}}{{end}}",
		templateStatus:  "<b>{{.Text}}</b>",
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
	return renderTemplate(style, templateStarted, messageData{MaxIterations: maxIterations, Objective: systemText(style, objective)})
}

func RenderStepMessage(format deliveryfmt.DeliveryFormat, iteration int, maxIterations int, step string, action string, body string) string {
	style := messageStyleForFormat(format)
	return renderTemplate(style, templateStep, messageData{MaxIterations: maxIterations, Iteration: iteration, Step: systemText(style, step), Action: systemText(style, action), Body: strings.TrimSpace(body)})
}

func RenderStatusMessage(format deliveryfmt.DeliveryFormat, text string) string {
	style := messageStyleForFormat(format)
	return renderTemplate(style, templateStatus, messageData{Text: systemText(style, text)})
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
	validation := strings.TrimSpace(outcome.Validation)
	routineSuccessfulOutcome := goalReached && exportStatusIsRoutineSuccess(exportStatus)
	verified := firstNonEmpty(outcome.Verified, "validator returned feedback")
	notVerified := firstNonEmpty(outcome.NotVerified, DefaultNotVerifiedText)
	nextAction := firstNonEmpty(outcome.NextAction, DefaultInspectNextAction)
	renderNotVerified := shouldRenderNotVerified(outcome.NotVerified)
	renderNextAction := shouldRenderNextAction(outcome.NextAction, goalReached, exportStatus)
	renderVerified := shouldRenderVerified(verified, routineSuccessfulOutcome)
	renderValidation := shouldRenderValidation(validation, goalReached)

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
			parts = append(parts, outcomeLine(format, "Export", "failed: "+systemText(messageStyleForFormat(format), firstNonEmpty(exportError, exportReason, "unknown error"))))
		default:
			parts = append(parts, outcomeLine(format, "Export", systemText(messageStyleForFormat(format), exportStatus)+"."))
		}
	}
	if whatWasDone != "" {
		if routineSuccessfulOutcome {
			parts = append(parts, strings.TrimSpace(whatWasDone))
		} else {
			parts = append(parts, outcomeBlock(format, "What was done", whatWasDone))
		}
	}
	if renderValidation && validation != "" {
		parts = append(parts, outcomeBlock(format, "Validation", validation))
	}
	if renderVerified && verified != "" {
		parts = append(parts, outcomeLine(format, "Verified", verified))
	}
	if renderNotVerified && notVerified != "" {
		parts = append(parts, outcomeLine(format, "Not verified", notVerified))
	}
	if renderNextAction && nextAction != "" {
		parts = append(parts, outcomeLine(format, "Next action", nextAction))
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
	case deliveryfmt.DeliveryFormatRichHTML:
		return messageStyleHTML
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

func systemText(style messageStyle, text string) string {
	text = strings.TrimSpace(text)
	if style == messageStyleHTML {
		return baldatelegram.EscapeHTML(text)
	}
	return text
}

func outcomeLine(format deliveryfmt.DeliveryFormat, label string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	style := messageStyleForFormat(format)
	switch style {
	case messageStyleMarkdown:
		return fmt.Sprintf("**%s:** %s", label, value)
	case messageStyleHTML:
		return fmt.Sprintf("<b>%s:</b> %s", baldatelegram.EscapeHTML(label), value)
	default:
		return label + ": " + value
	}
}

func outcomeBlock(format deliveryfmt.DeliveryFormat, label string, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	style := messageStyleForFormat(format)
	switch style {
	case messageStyleMarkdown:
		return fmt.Sprintf("**%s:**\n%s", label, body)
	case messageStyleHTML:
		return fmt.Sprintf("<b>%s:</b>\n%s", baldatelegram.EscapeHTML(label), body)
	default:
		return label + ":\n" + body
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shouldRenderNotVerified(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && !strings.EqualFold(trimmed, DefaultNotVerifiedText)
}

func shouldRenderVerified(value string, routineSuccessfulOutcome bool) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if !routineSuccessfulOutcome {
		return true
	}
	return !strings.EqualFold(trimmed, "validator returned pass")
}

func shouldRenderValidation(value string, goalReached bool) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if !goalReached {
		return true
	}
	return !validationIsRoutinePass(trimmed)
}

func validationIsRoutinePass(value string) bool {
	lowered := strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(lowered, "evidence:") || strings.Contains(lowered, "verdict: fail") || strings.Contains(lowered, "verdict fail") {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, ":", " ")))
	normalized = strings.Join(strings.Fields(normalized), " ")
	return strings.Contains(normalized, "verdict pass")
}

func shouldRenderNextAction(value string, goalReached bool, exportStatus string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if !goalReached {
		return true
	}
	if !strings.EqualFold(trimmed, DefaultExportedNextAction) {
		if goalReached && strings.TrimSpace(exportStatus) == GoalExportStatusNotExported && strings.EqualFold(trimmed, DefaultNotExportedNextAction) {
			return false
		}
		return true
	}
	switch strings.TrimSpace(exportStatus) {
	case GoalExportStatusFailed, GoalExportStatusNotExported:
		return true
	default:
		return false
	}
}

func exportStatusIsRoutineSuccess(status string) bool {
	switch strings.TrimSpace(status) {
	case "", GoalExportStatusExported, GoalExportStatusNotExported:
		return true
	default:
		return false
	}
}
