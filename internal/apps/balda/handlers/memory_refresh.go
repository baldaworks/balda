package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/memory"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"google.golang.org/adk/runner"
)

func prepareMemoryRunOptions(ctx context.Context, store *memory.Store, ts *baldasession.TopicSession) ([]runner.RunOption, error) {
	if store == nil || !store.MemoryEnabled() || ts == nil {
		return nil, nil
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot balda memory: %w", err)
	}
	seenVersion := int64(0)
	if value, ok := ts.RuntimeStateValue(memory.MemoryVersionStateKey); ok {
		seenVersion = memory.VersionFromState(value)
	}
	if snapshot.Version <= seenVersion {
		return nil, nil
	}
	stateDelta := map[string]any{
		memory.MemoryStateKey:        strings.TrimSpace(snapshot.Content),
		memory.MemoryVersionStateKey: memory.VersionStateValue(snapshot.Version),
	}
	return []runner.RunOption{runner.WithStateDelta(stateDelta)}, nil
}
