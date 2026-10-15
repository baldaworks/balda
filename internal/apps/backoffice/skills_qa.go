package backoffice

import (
	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"strings"
	"time"
)

var skillsQAEntries = []qaEntry{
	{name: "skills-pending-inventory", label: "Skills · pending inventory", templateName: webui.TemplateSkills, page: func() webui.Page {
		p := qaSkills()
		p.Skills.Pending = true
		p.Skills.Rows[2].Pending = true
		p.Skills.Rows[3].Pending = true
		return p
	}, gallery: true},
	{name: "skills/managed/managed-other-synthetic", templateName: webui.TemplateSkills, page: func() webui.Page { p := qaSkills(); p.Skills.Detail = &p.Skills.Rows[3]; return p }},
	{name: "skills", label: "Skills · mixed inventory", templateName: webui.TemplateSkills, page: qaSkills, gallery: true},
	{name: "skills-empty", label: "Skills · empty", templateName: webui.TemplateSkills, page: func() webui.Page { p := qaSkills(); p.Skills.Rows = nil; p.Skills.Diagnostics = nil; return p }, gallery: true},
	{name: "skills-long", label: "Skills · long names", templateName: webui.TemplateSkills, page: func() webui.Page {
		p := qaSkills()
		p.Skills.Rows[0].Name = strings.Repeat("long-synthetic-name-", 25)
		p.Skills.Rows[0].SourceLabel = strings.Repeat("long-source-", 25)
		return p
	}, gallery: true},
	{name: "skills/install", label: "Skills · installation form", templateName: webui.TemplateSkills, page: qaSkillInstall, gallery: true},
	{name: "skills-review", label: "Skills · install review", templateName: webui.TemplateSkills, page: qaSkillReview, gallery: true},
	{name: "skills-update", label: "Skills · disabled update review", templateName: webui.TemplateSkills, page: func() webui.Page {
		p := qaSkillReview()
		p.Skills.Update = true
		p.Skills.Review.Entry.Enabled = false
		p.RestartURL = "/skills/managed/managed-synthetic"
		return p
	}, gallery: true},
	{name: "skills/managed/managed-synthetic", label: "Skills · managed detail and confirmations", templateName: webui.TemplateSkills, page: qaSkillDetail, gallery: true},
	{name: "skills-unmanaged", label: "Skills · unmanaged detail", templateName: webui.TemplateSkills, page: func() webui.Page { p := qaSkills(); p.Skills.Detail = &p.Skills.Rows[1]; return p }, gallery: true},
	{name: "skills-plugin", label: "Skills · plugin detail", templateName: webui.TemplateSkills, page: func() webui.Page { p := qaSkills(); p.Skills.Detail = &p.Skills.Rows[0]; return p }, gallery: true},
	{name: "skills-disabled", label: "Skills · disabled detail", templateName: webui.TemplateSkills, page: func() webui.Page {
		p := qaSkillDetail()
		p.Skills.Detail.Enabled = false
		p.Skills.Detail.CurrentRevision = ""
		return p
	}, gallery: true},
	{name: "skills-detail-long", label: "Skills · long detail", templateName: webui.TemplateSkills, page: func() webui.Page {
		p := qaSkillDetail()
		p.Skills.Detail.Name = strings.Repeat("long-name-", 35)
		p.Skills.Detail.Description = "<script>synthetic escaped content</script>\n" + strings.Repeat("long-description-", 100)
		p.Skills.Detail.Revision = runtimecatalogcmd.RevisionID(strings.Repeat("abcdef0123456789", 12))
		return p
	}, gallery: true},
	{name: "skills-invalid", label: "Skills · invalid source", templateName: webui.TemplateSkills, status: 400, page: func() webui.Page {
		p := qaSkillInstall()
		p.Error = &webui.ErrorView{Heading: "Skill operation could not complete", Message: "Check Source, Ref and Skill path. Source is cleared after submission."}
		return p
	}, gallery: true},
	{name: "skills-conflict", label: "Skills · stale review", templateName: webui.TemplateSkills, status: 409, page: func() webui.Page {
		p := qaSkillDetail()
		p.Skills.MutationBlocked = true
		p.Error = &webui.ErrorView{Heading: "Skill operation could not complete", Message: "Skills changed. Reopen and review the current state before trying again."}
		return p
	}, gallery: true},
	{name: "skills-pending", label: "Skills · accepted pending", templateName: webui.TemplateSkills, status: 503, page: func() webui.Page {
		p := qaSkillDetail()
		p.Skills.Pending = true
		p.Skills.MutationBlocked = true
		p.Skills.Detail.Pending = true
		p.Skills.Detail.CurrentRevision = "sha256:synthetic-old-revision"
		p.Error = &webui.ErrorView{Heading: "Accepted; recovery pending", Message: "The change was saved. Recover publication; do not repeat the original action."}
		return p
	}, gallery: true},
	{name: "skills-unavailable", label: "Skills · unavailable", templateName: webui.TemplateSkills, status: 503, page: func() webui.Page {
		p := qaSkills()
		p.Skills.Unavailable = true
		p.Error = &webui.ErrorView{Heading: "Skills management unavailable", Message: "Reopen Skills and check current state."}
		return p
	}, gallery: true},
}

func qaSkills() webui.Page {
	page := qaOverview()
	page.Title, page.Current = "Skills · Balda", webui.LocationSkills
	page.Navigation = webui.Navigation(users.BackofficeCapabilities(usercmd.User{Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive}), page.Current)
	entries := []skillcmd.Entry{
		{Name: "writing", Description: "Synthetic plugin skill", ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "writing"}, Ownership: skillcmd.OwnershipPlugin, PluginID: "demo", SourceLabel: "Plugin: demo", Enabled: true, Revision: "sha256:synthetic-plugin", CurrentRevision: "sha256:synthetic-plugin"},
		{Name: "writing", Description: "Synthetic standalone skill", ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "agents-global"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "writing"}, Ownership: skillcmd.OwnershipUnmanaged, SourceLabel: "Global .agents/skills", Enabled: true, Revision: "sha256:synthetic-global", CurrentRevision: "sha256:synthetic-global"},
		{Name: "review", Description: "Review code and retain supporting resources. Synthetic preview only.", ID: runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-synthetic"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "review"}, Ownership: skillcmd.OwnershipManaged, SourceLabel: "Local directory", Enabled: true, Revision: "sha256:synthetic-managed-revision", CurrentRevision: "sha256:synthetic-managed-revision"},
	}
	duplicate := entries[2]
	duplicate.ID.Source.Name = "managed-other-synthetic"
	entries = append(entries, duplicate)
	page.Skills = webui.ProjectSkills(skillcmd.Inventory{Entries: entries, ExpectedState: "synthetic-state"})
	page.RestartURL, page.RestartLabel = "/skills", "Reopen Skills"
	return page
}
func qaSkillInstall() webui.Page {
	p := qaSkills()
	p.Skills.Install = true
	p.RestartURL, p.RestartLabel = "/skills/install", "Reopen install"
	return p
}
func qaSkillDetail() webui.Page {
	p := qaSkills()
	p.Skills.Detail = &p.Skills.Rows[2]
	p.RestartURL, p.RestartLabel = p.Skills.Detail.DetailPath, "Reopen skill"
	return p
}
func qaSkillReview() webui.Page {
	p := qaSkills()
	p.Skills.Review = &skillcmd.Candidate{Handle: "synthetic-invalid-handle", Entry: p.Skills.Rows[2].Entry, SkillPath: "skills/review", ResolvedCommit: strings.Repeat("a", 40), ExpiresAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	p.RestartURL, p.RestartLabel = "/skills/install", "Reopen install"
	return p
}
