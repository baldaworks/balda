package sessionmemory

func cloneTurn(turn Turn) Turn {
	turn.Messages = append([]Message(nil), turn.Messages...)
	return turn
}

func scopeViewFromSnapshot(snapshot ScopeSnapshot) ScopeView {
	return ScopeView{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         snapshot.Scope,
		Version:       snapshot.Version,
		Atoms:         activeAtoms(snapshot.Atoms),
		Scenarios:     activeScenarios(snapshot.Scenarios),
		Profiles:      activeProfiles(snapshot.Profiles),
	}
}

func cloneScopeSnapshot(snapshot ScopeSnapshot) ScopeSnapshot {
	snapshot.Sources = cloneSources(snapshot.Sources)
	snapshot.Atoms = cloneAtoms(snapshot.Atoms)
	snapshot.Scenarios = cloneScenarios(snapshot.Scenarios)
	snapshot.Profiles = cloneProfiles(snapshot.Profiles)
	return snapshot
}

func cloneOperationOutcome(outcome OperationOutcome) OperationOutcome {
	outcome.Revisions = append([]RevisionRef(nil), outcome.Revisions...)
	return outcome
}

func cloneForgetLookupResult(result ForgetLookupResult) ForgetLookupResult {
	result.Outcome = cloneForgetOutcome(result.Outcome)
	return result
}

func cloneForgetOutcome(outcome ForgetOutcome) ForgetOutcome {
	outcome.Sources = append([]SourceRef(nil), outcome.Sources...)
	outcome.Revisions = append([]RevisionRef(nil), outcome.Revisions...)
	return outcome
}

func cloneForgetSourceRequest(request ForgetSourceRequest) ForgetSourceRequest {
	request.ExpectedRevisions = append([]RevisionRef(nil), request.ExpectedRevisions...)
	return request
}

func cloneForgetScopeRequest(request ForgetScopeRequest) ForgetScopeRequest {
	request.ExpectedSources = append([]SourceRef(nil), request.ExpectedSources...)
	request.ExpectedRevisions = append([]RevisionRef(nil), request.ExpectedRevisions...)
	return request
}

func cloneSearchHits(hits []SearchHit) []SearchHit {
	cloned := append([]SearchHit(nil), hits...)
	for index := range cloned {
		if cloned[index].Atom != nil {
			atom := cloneAtoms([]Atom{*cloned[index].Atom})[0]
			cloned[index].Atom = &atom
		}
		if cloned[index].Scenario != nil {
			scenario := cloneScenarios([]Scenario{*cloned[index].Scenario})[0]
			cloned[index].Scenario = &scenario
		}
		if cloned[index].Profile != nil {
			profile := cloneProfiles([]Profile{*cloned[index].Profile})[0]
			cloned[index].Profile = &profile
		}
		if cloned[index].Score != nil {
			score := *cloned[index].Score
			cloned[index].Score = &score
		}
	}
	return cloned
}

func cloneTraceGraph(graph TraceGraph) TraceGraph {
	graph.Revisions = cloneSearchHits(graph.Revisions)
	graph.Sources = cloneSources(graph.Sources)
	return graph
}

func cloneCommitRequest(request CommitRequest) CommitRequest {
	request.Sources = cloneSources(request.Sources)
	request.Atoms = cloneAtoms(request.Atoms)
	request.Scenarios = cloneScenarios(request.Scenarios)
	request.Profiles = cloneProfiles(request.Profiles)
	request.Transitions = append([]RevisionTransition(nil), request.Transitions...)
	return request
}

func cloneSources(sources []SourceRecord) []SourceRecord {
	cloned := append([]SourceRecord(nil), sources...)
	for index := range cloned {
		if cloned[index].Turn != nil {
			turn := cloneTurn(*cloned[index].Turn)
			cloned[index].Turn = &turn
		}
		if cloned[index].ForgottenAt != nil {
			forgottenAt := *cloned[index].ForgottenAt
			cloned[index].ForgottenAt = &forgottenAt
		}
	}
	return cloned
}

func cloneAtoms(atoms []Atom) []Atom {
	cloned := append([]Atom(nil), atoms...)
	for index := range cloned {
		cloned[index].Meta = cloneRevisionMeta(cloned[index].Meta)
		if cloned[index].RelatedRevision != nil {
			ref := *cloned[index].RelatedRevision
			cloned[index].RelatedRevision = &ref
		}
	}
	return cloned
}

func activeAtoms(atoms []Atom) []Atom {
	active := make([]Atom, 0, len(atoms))
	for _, atom := range atoms {
		if atom.Meta.State == RevisionStateActive {
			active = append(active, atom)
		}
	}
	return cloneAtoms(active)
}

func cloneScenarios(scenarios []Scenario) []Scenario {
	cloned := append([]Scenario(nil), scenarios...)
	for index := range cloned {
		cloned[index].Meta = cloneRevisionMeta(cloned[index].Meta)
	}
	return cloned
}

func activeScenarios(scenarios []Scenario) []Scenario {
	active := make([]Scenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		if scenario.Meta.State == RevisionStateActive {
			active = append(active, scenario)
		}
	}
	return cloneScenarios(active)
}

func cloneProfiles(profiles []Profile) []Profile {
	cloned := append([]Profile(nil), profiles...)
	for index := range cloned {
		cloned[index].Meta = cloneRevisionMeta(cloned[index].Meta)
	}
	return cloned
}

func activeProfiles(profiles []Profile) []Profile {
	active := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Meta.State == RevisionStateActive {
			active = append(active, profile)
		}
	}
	return cloneProfiles(active)
}

func cloneRevisionMeta(meta RevisionMeta) RevisionMeta {
	meta.Provenance.RawSources = append([]SourceRef(nil), meta.Provenance.RawSources...)
	meta.Provenance.ParentRevisions = append([]RevisionRef(nil), meta.Provenance.ParentRevisions...)
	if meta.Supersedes != nil {
		ref := *meta.Supersedes
		meta.Supersedes = &ref
	}
	return meta
}
