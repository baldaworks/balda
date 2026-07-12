package sessionturnapp

import (
	"github.com/normahq/balda/internal/apps/balda/progress"
	adksession "google.golang.org/adk/v2/session"
)

func baldaPlanProgress(ev *adksession.Event) (progress.PlanSnapshot, string, bool) {
	snapshot, ok := progress.ParsePlanUpdate(ev)
	if !ok {
		return progress.PlanSnapshot{}, "", false
	}
	return snapshot, snapshot.PlainText(), true
}
