package pluginbackofficeapp

import "github.com/baldaworks/balda/internal/apps/balda/plugincmd"

// CatalogStatus is the catalog-owned status facade consumed by host composition.
type CatalogStatus interface {
	PluginStatus(name, revision string) plugincmd.RuntimeStatus
}

// CatalogHealth adapts the catalog owner's evidence to the browser adapter.
type CatalogHealth struct{ Catalog CatalogStatus }

// Status reads current observations without changing catalog selection.
func (h CatalogHealth) Status(name, revision string) plugincmd.RuntimeStatus {
	if h.Catalog == nil {
		return plugincmd.RuntimeStatus{DiagnosticCodes: []string{"catalog_unavailable"}}
	}
	return h.Catalog.PluginStatus(name, revision)
}
