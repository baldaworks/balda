package backoffice

import (
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

var hooksQAEntries = []qaEntry{
	{name: "hooks-tool-before", label: "Agent hooks · tool policy", templateName: webui.TemplateHooks, page: func() webui.Page {
		p := qaHookEditor()
		p.Hooks.Editor.Definition.Event = hookcmd.PreToolUse
		p.Hooks.Editor.Definition.Matcher = "^(Bash|exec_command|bash)$"
		return p
	}, gallery: true},
	{name: "hooks-tool-after", label: "Agent hooks · tool result", templateName: webui.TemplateHooks, page: func() webui.Page {
		p := qaHookEditor()
		p.Hooks.Editor.Definition.Event = hookcmd.PostToolUse
		return p
	}, gallery: true},
	{name: "hooks-unsupported", label: "Agent hooks · unavailable native adapter", templateName: webui.TemplateHooks, status: http.StatusBadRequest, page: func() webui.Page {
		p := qaHookEditor()
		p.Hooks.Editor.Definition.Event = hookcmd.PreToolUse
		p.Hooks.Capabilities = "Native tool hooks are unavailable for the configured provider. Lifecycle hooks remain available."
		p.Error = &webui.ErrorView{Heading: "Native tool hooks unavailable", Message: "Review the configured provider before enabling this hook."}
		return p
	}, gallery: true},
	{name: "hooks", label: "Agent hooks · inventory", templateName: webui.TemplateHooks, page: qaHooks, gallery: true},
	{name: "hooks-empty", label: "Agent hooks · empty", templateName: webui.TemplateHooks, page: func() webui.Page { p := qaHooks(); p.Hooks.Rows = nil; return p }, gallery: true},
	{name: "hooks-long", label: "Agent hooks · long names", templateName: webui.TemplateHooks, page: func() webui.Page {
		p := qaHooks()
		p.Hooks.Rows[0].Name = strings.Repeat("long-", 12)
		p.Hooks.Rows[0].Provider = strings.Repeat("provider-", 15)
		return p
	}, gallery: true},
	{name: "hooks/new", label: "Agent hooks · create", templateName: webui.TemplateHooks, page: func() webui.Page {
		p := qaHooks()
		p.Hooks.Editor = webui.ProjectHookEditor(hookcmd.Definition{Event: hookcmd.UserPromptSubmit, TimeoutSeconds: 10}, 3, true)
		return p
	}, gallery: true},
	{name: "hooks/guard", label: "Agent hooks · edit", templateName: webui.TemplateHooks, page: qaHookEditor, gallery: true},
	{name: "hooks-invalid", label: "Agent hooks · validation", templateName: webui.TemplateHooks, status: http.StatusBadRequest, page: func() webui.Page {
		p := qaHookEditor()
		p.Hooks.Editor.ArgsJSON = "submitted invalid arguments"
		p.Error = &webui.ErrorView{Heading: "Check the hook configuration", Message: "Arguments must be a JSON array of strings."}
		return p
	}, gallery: true},
	{name: "hooks-conflict", label: "Agent hooks · conflict", templateName: webui.TemplateHooks, status: http.StatusConflict, page: func() webui.Page {
		p := qaHookEditor()
		p.Error = &webui.ErrorView{Heading: "Agent hooks changed", Message: "Reopen this hook and review its current configuration before saving."}
		return p
	}, gallery: true},
	{name: "hooks-unavailable", label: "Agent hooks · unavailable", templateName: webui.TemplateHooks, status: http.StatusServiceUnavailable, page: func() webui.Page {
		p := qaHooks()
		p.Error = &webui.ErrorView{Heading: "Agent hooks unavailable", Message: "Reopen this page and check the saved configuration before retrying."}
		return p
	}, gallery: true},
}

func qaHooks() webui.Page {
	page := qaOverview()
	page.Title, page.Current = "Agent hooks · Balda", webui.LocationHooks
	page.Navigation = webui.Navigation(users.BackofficeCapabilities(usercmd.User{Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive}), page.Current)
	page.Hooks = webui.ProjectHooks(hookcmd.Record{Version: 3, Definitions: []hookcmd.Definition{
		{Name: "guard", Event: hookcmd.UserPromptSubmit, Enabled: true, Command: "/usr/bin/python3", Args: []string{"/synthetic/hooks/guard.py"}, TimeoutSeconds: 10},
		{Name: "session-note", Event: hookcmd.SessionEnd, Command: "/usr/bin/true", TimeoutSeconds: 2},
	}})
	page.Hooks.Capabilities = "Native PreToolUse and PostToolUse adapters are configured for provider qa-provider. Activation requires a new session: use /reset for an existing session. In-flight tool calls are unaffected. Native engine deadlines still apply; Claude's outer timer is about 24.8 days. Unsupported native versions fail explicitly."
	return page
}

func qaHookEditor() webui.Page {
	page := qaHooks()
	page.Hooks.Editor = webui.ProjectHookEditor(hookcmd.Definition{Name: "guard", Event: hookcmd.UserPromptSubmit, Enabled: true, Command: "/usr/bin/python3", Args: []string{"/synthetic/hooks/guard.py"}, TimeoutSeconds: 10}, 3, false)
	return page
}
