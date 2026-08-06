package sessionmemory

// Validate verifies a supported forgetting operation kind.
func (k ForgetKind) Validate() error {
	switch k {
	case ForgetKindSource, ForgetKindScope:
		return nil
	default:
		return invalidDerived("unsupported forget kind")
	}
}

// Validate verifies a source-forgetting command.
func (c ForgetSourceCommand) Validate() error {
	if c.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported forget command schema version")
	}
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if c.ForgottenAt.IsZero() {
		return invalidDerived("forget command timestamp is required")
	}
	return nil
}

// Validate verifies an exact-scope forgetting command.
func (c ForgetScopeCommand) Validate() error {
	if c.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported forget command schema version")
	}
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(c.RequestID) {
		return invalidDerived("scope forget request id is required")
	}
	if c.ForgottenAt.IsZero() {
		return invalidDerived("forget command timestamp is required")
	}
	return nil
}

// Validate verifies a content-free forgetting outcome.
func (o ForgetOutcome) Validate() error {
	if o.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported forget outcome schema version")
	}
	if !isCanonicalID(o.OperationID) {
		return invalidDerived("forget outcome operation id is required")
	}
	if err := o.Kind.Validate(); err != nil {
		return err
	}
	if err := o.Scope.Validate(); err != nil {
		return err
	}
	if o.ScopeVersion == 0 {
		return invalidDerived("forget outcome scope version is required")
	}
	if len(o.Sources)+len(o.Revisions) > MaxSnapshotItems {
		return limitExceeded("forget outcome exceeds the item limit")
	}
	if o.Kind == ForgetKindSource && len(o.Sources) != 1 {
		return invalidDerived("source forget outcome requires exactly one source")
	}
	seen := make(map[string]struct{}, len(o.Sources))
	for _, source := range o.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != o.Scope {
			return PermanentError(CodeScopeViolation, "forget outcome contains a foreign source", nil)
		}
		if _, ok := seen[source.ExportID]; ok {
			return invalidDerived("forget outcome contains a duplicate source")
		}
		seen[source.ExportID] = struct{}{}
	}
	return validateUniqueRevisionRefs(o.Revisions, "forget outcome")
}

func validateUniqueRevisionRefs(refs []RevisionRef, subject string) error {
	if len(refs) > MaxSnapshotItems {
		return limitExceeded(subject + " exceeds the revision limit")
	}
	seen := make(map[RevisionRef]struct{}, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, ok := seen[ref]; ok {
			return invalidDerived(subject + " contains a duplicate revision")
		}
		seen[ref] = struct{}{}
	}
	return nil
}
