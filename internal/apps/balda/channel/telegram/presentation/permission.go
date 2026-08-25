package presentation

import (
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
)

func RenderPermission(body permissioncmd.Request) string {
	return permissionfmt.RenderTelegramMarkdown(body)
}
