package sessionmemory

// Config lowers derived processing bounds. Zero values select hard defaults.
// A caller cannot raise a bound above the package hard ceiling.
type Config struct {
	MaxTurnTextBytes       int
	MaxCandidateCount      int
	MaxSourcesPerRevision  int
	MaxDerivedTextBytes    int
	MaxSnapshotItems       int
	MaxTraceNodes          int
	MaxSearchResults       int
	MaxSearchResponseBytes int
}

// DefaultConfig returns all package hard ceilings.
func DefaultConfig() Config {
	return Config{
		MaxTurnTextBytes:       MaxDerivedTurnTextBytes,
		MaxCandidateCount:      MaxCandidateCount,
		MaxSourcesPerRevision:  MaxSourcesPerRevision,
		MaxDerivedTextBytes:    MaxDerivedTextBytes,
		MaxSnapshotItems:       MaxSnapshotItems,
		MaxTraceNodes:          MaxTraceNodes,
		MaxSearchResults:       MaxSearchLimit,
		MaxSearchResponseBytes: MaxSearchResponseBytes,
	}
}

// Validate verifies that every configured bound is positive and at or below
// its package hard ceiling. Zero-valued fields select their default.
func (c Config) Validate() error {
	_, err := normalizeConfig(c)
	return err
}

func normalizeConfig(config Config) (Config, error) {
	defaults := DefaultConfig()
	fields := []struct {
		name         string
		value        *int
		defaultValue int
		ceiling      int
	}{
		{name: "turn text", value: &config.MaxTurnTextBytes, defaultValue: defaults.MaxTurnTextBytes, ceiling: MaxDerivedTurnTextBytes},
		{name: "candidate count", value: &config.MaxCandidateCount, defaultValue: defaults.MaxCandidateCount, ceiling: MaxCandidateCount},
		{name: "sources per revision", value: &config.MaxSourcesPerRevision, defaultValue: defaults.MaxSourcesPerRevision, ceiling: MaxSourcesPerRevision},
		{name: "derived text", value: &config.MaxDerivedTextBytes, defaultValue: defaults.MaxDerivedTextBytes, ceiling: MaxDerivedTextBytes},
		{name: "snapshot items", value: &config.MaxSnapshotItems, defaultValue: defaults.MaxSnapshotItems, ceiling: MaxSnapshotItems},
		{name: "trace nodes", value: &config.MaxTraceNodes, defaultValue: defaults.MaxTraceNodes, ceiling: MaxTraceNodes},
		{name: "search results", value: &config.MaxSearchResults, defaultValue: defaults.MaxSearchResults, ceiling: MaxSearchLimit},
		{name: "search response bytes", value: &config.MaxSearchResponseBytes, defaultValue: defaults.MaxSearchResponseBytes, ceiling: MaxSearchResponseBytes},
	}
	for _, field := range fields {
		if *field.value == 0 {
			*field.value = field.defaultValue
		}
		if *field.value < 1 || *field.value > field.ceiling {
			return Config{}, limitExceeded(field.name + " configuration is outside the allowed range")
		}
	}
	return config, nil
}
