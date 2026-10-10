// Package httpfx composes Balda's inbound HTTP handlers without owning their
// authentication or admission policy.
package httpfx

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// WebhookLookup checks whether a path currently names an active webhook.
// Its result can change after startup when managed routes are edited.
type WebhookLookup func(context.Context, string) (bool, error)

type route struct {
	owner   string
	handler http.Handler
}

// Registry checks path ownership before assembling the shared HTTP handler.
// Build it before listening; do not mutate it after calling Handler.
type Registry struct {
	backofficePath string
	webhookPath    string
	gatewayPath    string
	backoffice     route
	webhooks       route
	lookup         WebhookLookup
	exact          map[string]route
}

// NewRegistry reserves the three sibling HTTP areas below basePath.
func NewRegistry(basePath string) (*Registry, error) {
	if basePath != "" && (basePath == "/" || !strings.HasPrefix(basePath, "/") || strings.HasSuffix(basePath, "/") || strings.ContainsAny(basePath, "\\?#% \t\r\n") || strings.Contains(basePath, "//") || path.Clean(basePath) != basePath) {
		return nil, fmt.Errorf("invalid shared http base path %q", basePath)
	}
	return &Registry{
		backofficePath: basePath + "/backoffice",
		webhookPath:    basePath + "/webhooks",
		gatewayPath:    basePath + "/gateway",
		exact:          make(map[string]route),
	}, nil
}

// AddBackoffice mounts the already secured browser handler below /backoffice.
func (r *Registry) AddBackoffice(owner string, handler http.Handler) error {
	if err := validateContribution(owner, handler); err != nil {
		return err
	}
	if r.backoffice.handler != nil {
		return routeConflict(r.backofficePath, owner, r.backoffice.owner)
	}
	r.backoffice = route{owner: owner, handler: handler}
	return nil
}

// AddGateway registers one exact callback path, including an existing alias.
func (r *Registry) AddGateway(owner, routePath string, handler http.Handler) error {
	if err := validateContribution(owner, handler); err != nil {
		return err
	}
	if err := validateRoutePath(routePath); err != nil {
		return err
	}
	if existing, exists := r.exact[routePath]; exists {
		return routeConflict(routePath, owner, existing.owner)
	}
	if within(routePath, r.backofficePath) {
		return routeConflict(routePath, owner, areaOwner(r.backoffice.owner, "backoffice"))
	}
	if within(routePath, r.webhookPath) {
		return routeConflict(routePath, owner, areaOwner(r.webhooks.owner, "webhooks"))
	}
	return r.addExact(owner, routePath, handler)
}

// AddWebhook registers an active exact path, including a retained custom path.
// The supplied handler keeps its existing authentication and admission checks.
func (r *Registry) AddWebhook(owner, routePath string, handler http.Handler) error {
	if err := validateContribution(owner, handler); err != nil {
		return err
	}
	if err := validateRoutePath(routePath); err != nil {
		return err
	}
	if existing, exists := r.exact[routePath]; exists {
		return routeConflict(routePath, owner, existing.owner)
	}
	if within(routePath, r.backofficePath) {
		return routeConflict(routePath, owner, areaOwner(r.backoffice.owner, "backoffice"))
	}
	if within(routePath, r.gatewayPath) {
		return routeConflict(routePath, owner, "gateway")
	}
	return r.addExact(owner, routePath, handler)
}

// SetWebhookLookup routes newly active canonical and legacy paths without
// restart. Unknown paths never reach the supplied receiver.
func (r *Registry) SetWebhookLookup(owner string, handler http.Handler, lookup WebhookLookup) error {
	if err := validateContribution(owner, handler); err != nil {
		return err
	}
	if lookup == nil {
		return fmt.Errorf("webhook lookup is required for %q", owner)
	}
	if r.webhooks.handler != nil {
		return routeConflict(r.webhookPath, owner, r.webhooks.owner)
	}
	r.webhooks, r.lookup = route{owner: owner, handler: handler}, lookup
	return nil
}

func (r *Registry) addExact(owner, routePath string, handler http.Handler) error {
	if existing, exists := r.exact[routePath]; exists {
		return routeConflict(routePath, owner, existing.owner)
	}
	r.exact[routePath] = route{owner: owner, handler: handler}
	return nil
}

// Handler returns the composed router. It preserves URL.Path for each owner.
func (r *Registry) Handler() http.Handler {
	exact := make(map[string]route, len(r.exact))
	for routePath, contribution := range r.exact {
		exact[routePath] = contribution
	}
	backoffice, webhooks, lookup := r.backoffice, r.webhooks, r.lookup
	backofficePath, gatewayPath := r.backofficePath, r.gatewayPath
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if within(path, backofficePath) {
			if backoffice.handler != nil {
				backoffice.handler.ServeHTTP(w, request)
			} else {
				http.NotFound(w, request)
			}
			return
		}
		if contribution, ok := exact[path]; ok {
			contribution.handler.ServeHTTP(w, request)
			return
		}
		if within(path, gatewayPath) {
			http.NotFound(w, request)
			return
		}
		if webhooks.handler != nil {
			active, err := lookup(request.Context(), path)
			if err != nil {
				http.Error(w, "webhook route unavailable", http.StatusServiceUnavailable)
				return
			}
			if active {
				webhooks.handler.ServeHTTP(w, request)
				return
			}
		}
		http.NotFound(w, request)
	})
}

func within(path, area string) bool {
	return path == area || strings.HasPrefix(path, area+"/")
}

func validateContribution(owner string, handler http.Handler) error {
	if strings.TrimSpace(owner) == "" || handler == nil {
		return fmt.Errorf("http route owner and handler are required")
	}
	return nil
}

func validateRoutePath(routePath string) error {
	if routePath == "" || !strings.HasPrefix(routePath, "/") || strings.ContainsAny(routePath, "?#\r\n") {
		return fmt.Errorf("invalid http route path %q", routePath)
	}
	return nil
}

func routeConflict(path, owner, existing string) error {
	return fmt.Errorf("http route %q owned by %q conflicts with %q", path, owner, existing)
}

func areaOwner(owner, fallback string) string {
	if owner != "" {
		return owner
	}
	return fallback
}
