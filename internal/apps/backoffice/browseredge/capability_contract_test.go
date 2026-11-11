package browseredge

import "github.com/baldaworks/balda/internal/apps/backoffice/managementclient"

// Keep the cutover boundary explicit: the production generated-client adapter
// must satisfy every capability consumed by the standalone browser edge. The
// domain tests exercise the individual outcomes; these assertions prevent a
// capability from silently falling back to the unavailable branch.
var (
	_ Service         = (*managementclient.Client)(nil)
	_ SystemService   = (*managementclient.Client)(nil)
	_ MemoryService   = (*managementclient.Client)(nil)
	_ ArchiveService  = (*managementclient.Client)(nil)
	_ UserService     = (*managementclient.Client)(nil)
	_ AuditService    = (*managementclient.Client)(nil)
	_ ProviderService = (*managementclient.Client)(nil)
	_ MCPService      = (*managementclient.Client)(nil)
	_ ScheduleService = (*managementclient.Client)(nil)
	_ WebhookService  = (*managementclient.Client)(nil)
	_ AliasService    = (*managementclient.Client)(nil)
	_ HookService     = (*managementclient.Client)(nil)
	_ PluginService   = (*managementclient.Client)(nil)
	_ SkillService    = (*managementclient.Client)(nil)
)
