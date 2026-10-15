package webhook

import "testing"

func TestSlugRouteConfig(t *testing.T) {
	cfg := Config{Enabled: true, Routes: map[string]RouteConfig{"orders": {PromptTemplate: "{{.RawBody}}"}}}
	if _, err := normalizeConfig(cfg); err != nil {
		t.Fatalf("path-free route config: %v", err)
	}
}
