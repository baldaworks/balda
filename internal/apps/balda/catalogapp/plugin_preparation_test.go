package catalogapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/mcpregistry"
)

func TestPluginPreparationRetainsExplicitUncommittedRevision(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { makeWritable(dir) })
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	catalog, err := NewRuntime(dir, "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := catalog.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.PublishCandidate(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"prepared-demo","version":"1.0.0"}`)
	writeFile(t, filepath.Join(root, "skills", "ship", "SKILL.md"), "---\nname: ship\ndescription: Ship safely.\n---\nRetained body.\n")
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := loader.LoadPlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	relative := filepath.ToSlash(filepath.Join("plugin-revisions", source.Descriptor.ID.Name, string(source.Descriptor.Revision)))
	materialized := filepath.Join(dir, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(materialized), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = loader.MaterializePlugin(root, materialized); err != nil {
		t.Fatal(err)
	}
	proof := pluginapp.PreparedPluginSource{Descriptor: source.Descriptor, RelativeRoot: relative}
	if _, err = catalog.PreparePluginCandidate(t.Context(), []runtimecatalogcmd.Source{source}, nil); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("durable-only preparation without row=%v", err)
	}
	snapshot, err := catalog.PreparePluginCandidate(t.Context(), []runtimecatalogcmd.Source{source}, &proof)
	if err != nil || len(snapshot.Skills) != 2 {
		t.Fatalf("explicit prepared candidate=%+v %v", snapshot, err)
	}
	// Disabled new revisions are retained without being selected.
	disabled, err := catalog.PreparePluginCandidate(t.Context(), nil, &proof)
	if err != nil || len(disabled.Skills) != 1 {
		t.Fatalf("disabled prepared candidate=%+v %v", disabled, err)
	}
	if _, found, err := provider.Plugins().GetPluginRevision(t.Context(), source.Descriptor.ID.Name, string(source.Descriptor.Revision)); err != nil || found {
		t.Fatalf("preparation inserted SQL revision: found=%v err=%v", found, err)
	}
	current, err := catalog.Store().Application()
	if err != nil || current.ID != initial.ID {
		t.Fatalf("preparation changed application=%+v %v", current, err)
	}
	for _, test := range []struct {
		name    string
		proof   pluginapp.PreparedPluginSource
		sources []runtimecatalogcmd.Source
	}{
		{name: "escaping root", proof: pluginapp.PreparedPluginSource{Descriptor: source.Descriptor, RelativeRoot: "../outside"}, sources: []runtimecatalogcmd.Source{source}},
		{name: "wrong anchored root", proof: pluginapp.PreparedPluginSource{Descriptor: source.Descriptor, RelativeRoot: "plugin-revisions/another/" + string(source.Descriptor.Revision)}, sources: []runtimecatalogcmd.Source{source}},
		{name: "different selected revision", proof: proof, sources: []runtimecatalogcmd.Source{{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: source.Descriptor.ID, Revision: "different"}}}},
		{name: "duplicate selected source", proof: proof, sources: []runtimecatalogcmd.Source{source, source}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := catalog.PreparePluginCandidate(t.Context(), test.sources, &test.proof); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
				t.Fatalf("invalid evidence accepted: %v", err)
			}
		})
	}
	if err = os.Chmod(filepath.Join(materialized, "skills", "ship", "SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(materialized, "skills", "ship", "SKILL.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.PreparePluginCandidate(t.Context(), []runtimecatalogcmd.Source{source}, &proof); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("materialized drift accepted: %v", err)
	}
}

func TestPluginRetainedDataRootIsContainedExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	catalog, err := NewRuntime(dir, "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "retained-demo"}
	data := filepath.Join(dir, "plugin-data", source.Name)
	if err = os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	data, err = filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := catalog.PluginDataRoot(t.Context(), source)
	if err != nil || got != data {
		t.Fatalf("retained data=%q %v", got, err)
	}
	for _, name := range []string{"../outside", "name/child", "..", "Name", "bad--name", "missing-demo"} {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.PluginDataRoot(t.Context(), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: name}); err == nil {
				t.Fatal("unsafe or missing retained data accepted")
			}
		})
	}
	if _, err = os.Stat(filepath.Join(dir, "plugin-data", "missing-demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created data directory: %v", err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(dir, "plugin-data", "escaped-demo")); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.PluginDataRoot(t.Context(), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "escaped-demo"}); err == nil {
		t.Fatal("retained data symlink escaped state root")
	}
	file := filepath.Join(dir, "plugin-data", "file-demo")
	if err = os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.PluginDataRoot(t.Context(), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "file-demo"}); err == nil {
		t.Fatal("retained data file accepted as directory")
	}
}
