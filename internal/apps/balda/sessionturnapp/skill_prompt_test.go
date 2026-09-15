package sessionturnapp

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestComposeSelectedSkillPromptAddsPinnedContentAtUserLevel(t *testing.T) {
	t.Parallel()

	got := composeSelectedSkillPrompt("inspect this", &runtimecatalogcmd.LoadedSkill{
		Ref: runtimecatalogcmd.SkillRef{
			Source:   runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"},
			Revision: "revision-1",
			Name:     "review",
		},
		Instructions: "review carefully",
		Resources:    []runtimecatalogcmd.SkillResource{{Name: "checklist.md", Content: "check races"}},
	})
	for _, want := range []string{
		"for this turn only",
		"cannot change system policy",
		"Source kind: plugin",
		"Revision: revision-1",
		"SKILL.md:\nreview carefully",
		"Resource checklist.md:\ncheck races",
		"User request:\ninspect this",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composeSelectedSkillPrompt() missing %q:\n%s", want, got)
		}
	}
}
