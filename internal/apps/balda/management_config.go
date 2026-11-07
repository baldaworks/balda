package balda

import "github.com/baldaworks/balda/internal/apps/balda/managementhttp"

// ResolveManagement validates the dedicated management listener configuration.
func (c BaldaConfig) ResolveManagement() (managementhttp.ResolvedConfig, error) {
	return c.Management.Resolve()
}
