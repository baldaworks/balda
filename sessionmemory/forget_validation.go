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

// Validate verifies a forgetting lookup identity.
func (l ForgetLookup) Validate() error {
	if l.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported forget lookup schema version")
	}
	if !isCanonicalID(l.OperationID) {
		return invalidDerived("forget lookup operation id is required")
	}
	if err := l.Kind.Validate(); err != nil {
		return err
	}
	return l.Scope.Validate()
}

// Validate verifies a lookup result against its exact request.
func (r ForgetLookupResult) Validate(lookup ForgetLookup) error {
	if err := lookup.Validate(); err != nil {
		return err
	}
	if !r.Found {
		if !forgetOutcomeIsZero(r.Outcome) {
			return invalidDerived("missing forget lookup cannot contain an outcome")
		}
		return nil
	}
	if err := r.Outcome.Validate(); err != nil {
		return err
	}
	if r.Outcome.OperationID != lookup.OperationID || r.Outcome.Kind != lookup.Kind || r.Outcome.Scope != lookup.Scope {
		return PermanentError(CodeScopeViolation, "forget lookup outcome does not match the request", nil)
	}
	return nil
}

// Validate verifies one atomic source-forgetting Store request.
func (r ForgetSourceRequest) Validate() error {
	if err := validateForgetRequestBase(r.SchemaVersion, r.OperationID, r.Scope, r.ForgottenAt.IsZero()); err != nil {
		return err
	}
	if err := r.Source.Validate(); err != nil {
		return err
	}
	if r.Source.Scope != r.Scope {
		return PermanentError(CodeScopeViolation, "forgotten source scope does not match the operation", nil)
	}
	return validateUniqueRevisionRefs(r.ExpectedRevisions, "source forget")
}

// Validate verifies one atomic exact-scope forgetting Store request.
func (r ForgetScopeRequest) Validate() error {
	if err := validateForgetRequestBase(r.SchemaVersion, r.OperationID, r.Scope, r.ForgottenAt.IsZero()); err != nil {
		return err
	}
	if len(r.ExpectedSources)+len(r.ExpectedRevisions) > MaxSnapshotItems {
		return limitExceeded("scope forget exceeds the item limit")
	}
	seen := make(map[string]struct{}, len(r.ExpectedSources))
	for _, source := range r.ExpectedSources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != r.Scope {
			return PermanentError(CodeScopeViolation, "scope forget contains a foreign source", nil)
		}
		if _, ok := seen[source.ExportID]; ok {
			return invalidDerived("scope forget contains a duplicate source")
		}
		seen[source.ExportID] = struct{}{}
	}
	return validateUniqueRevisionRefs(r.ExpectedRevisions, "scope forget")
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

func validateForgetRequestBase(schemaVersion, operationID string, scope Scope, missingTime bool) error {
	if schemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported forget request schema version")
	}
	if !isCanonicalID(operationID) {
		return invalidDerived("forget request operation id is required")
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if missingTime {
		return invalidDerived("forget request timestamp is required")
	}
	return nil
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

func forgetOutcomeIsZero(outcome ForgetOutcome) bool {
	return outcome.SchemaVersion == "" && outcome.OperationID == "" && outcome.Kind == "" &&
		outcome.Scope == (Scope{}) && outcome.ScopeVersion == 0 && len(outcome.Sources) == 0 && len(outcome.Revisions) == 0
}
