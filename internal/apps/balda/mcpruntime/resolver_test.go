package mcpruntime

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestStaticResolverRequiresExactConfiguredRevision(t *testing.T) {
	t.Parallel()

	id := runtimecatalogcmd.ContributionID{
		Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: "configured"},
		Kind:   runtimecatalogcmd.ContributionKindMCPServer,
		Name:   "tools",
	}
	resolver := NewStaticResolver(map[runtimecatalogcmd.ContributionID]StaticLaunchEntry{
		id: {Revision: "revision-one", Config: LaunchConfig{Transport: "stdio", Command: "server"}},
	})
	descriptor := runtimecatalogcmd.MCPServerDescriptor{ID: id, Revision: "revision-one", Name: "tools", Transport: "stdio"}
	config, err := resolver.ResolveLaunch(context.Background(), descriptor)
	if err != nil || config.Command != "server" {
		t.Fatalf("ResolveLaunch(exact) = %+v, %v", config, err)
	}
	descriptor.Revision = "revision-two"
	if _, err := resolver.ResolveLaunch(context.Background(), descriptor); err != runtimecatalogcmd.ErrRevisionUnavailable {
		t.Fatalf("ResolveLaunch(other revision) error = %v", err)
	}
}

func TestRoutedResolverDoesNotFallbackAcrossSourceKinds(t *testing.T) {
	t.Parallel()

	plugin := mcpDescriptor("revision")
	configured := fakeLaunchResolver{}
	resolver := RoutedResolver{Configured: configured}
	if _, err := resolver.ResolveLaunch(context.Background(), plugin); err == nil {
		t.Fatal("ResolveLaunch(plugin) fell back to configured resolver")
	}
}
