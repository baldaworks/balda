// Package skillcliapp adapts standalone skill lifecycle operations for local
// command-line clients without exposing private source or browser metadata.
package skillcliapp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// Lifecycle is the existing standalone-skill policy used by this adapter.
type Lifecycle interface {
	Inventory(ctx context.Context, authority skillcmd.Authority) (skillcmd.Inventory, error)
	ReviewInstall(ctx context.Context, request skillapp.InstallRequest) (skillcmd.Candidate, error)
	ReviewUpdate(ctx context.Context, mutation skillcmd.Mutation) (skillcmd.Candidate, error)
	Accept(ctx context.Context, handle string, authority skillcmd.Authority) (skillcmd.Outcome, error)
	Change(ctx context.Context, operation skillcmd.Operation, mutation skillcmd.Mutation) (skillcmd.Outcome, error)
	Refresh(ctx context.Context, mutation skillcmd.Mutation) (skillcmd.Outcome, error)
	RetryPublication(ctx context.Context, authority skillcmd.Authority) (skillcmd.Outcome, error)
}

// Confirm approves one bounded, public review. It must not inspect source
// origins or lifecycle internals.
type Confirm func(Review) (bool, error)

// App exposes transport-neutral CLI operations.
type App struct {
	lifecycle Lifecycle
	now       func() time.Time
}

// New constructs a CLI adapter over the canonical lifecycle.
func New(lifecycle Lifecycle) (*App, error) {
	if lifecycle == nil {
		return nil, newError(CodeUnavailable)
	}
	return &App{lifecycle: lifecycle, now: time.Now}, nil
}

// List returns public metadata for every visible skill contribution.
func (a *App) List(ctx context.Context) (Inventory, error) {
	inventory, err := a.inventory(ctx)
	if err != nil {
		return Inventory{}, err
	}
	return projectInventory(inventory), nil
}

// Show resolves an exact public ID or a unique skill name.
func (a *App) Show(ctx context.Context, selector string) (Entry, error) {
	inventory, err := a.inventory(ctx)
	if err != nil {
		return Entry{}, err
	}
	entry, err := resolve(inventory.Entries, selector)
	if err != nil {
		return Entry{}, err
	}
	return projectEntry(entry), nil
}

// InstallRequest contains transient source input. It is passed directly to
// skillapp and never copied into CLI output.
type InstallRequest struct {
	Source string
	Ref    string
	Path   string
}

// Install reviews exact source bytes, asks for confirmation, then accepts the
// opaque reviewed handle.
func (a *App) Install(ctx context.Context, request InstallRequest, confirm Confirm) (Result, error) {
	authority := a.authority()
	inventory, err := a.lifecycle.Inventory(ctx, authority)
	if err != nil {
		return Result{}, mapError(err)
	}
	candidate, err := a.lifecycle.ReviewInstall(ctx, skillapp.InstallRequest{
		Kind: InstallKind(request.Source), Source: request.Source, Ref: request.Ref, Path: request.Path,
		ExpectedState: inventory.ExpectedState, Authority: authority,
	})
	if err != nil {
		return Result{}, mapError(err)
	}
	review := candidateReview(skillcmd.OperationInstall, candidate)
	if err := approve(review, confirm); err != nil {
		return Result{}, err
	}
	outcome, err := a.lifecycle.Accept(ctx, candidate.Handle, authority)
	return projectResult(skillcmd.OperationInstall, outcome), mapError(err)
}

// Update reviews the retained private origin for one managed skill before
// accepting it.
func (a *App) Update(ctx context.Context, selector string, confirm Confirm) (Result, error) {
	authority := a.authority()
	inventory, entry, err := a.managed(ctx, authority, selector)
	if err != nil {
		return Result{}, err
	}
	candidate, err := a.lifecycle.ReviewUpdate(ctx, mutation(inventory, entry, authority))
	if err != nil {
		return Result{}, mapError(err)
	}
	review := candidateReview(skillcmd.OperationUpdate, candidate)
	if err := approve(review, confirm); err != nil {
		return Result{}, err
	}
	outcome, err := a.lifecycle.Accept(ctx, candidate.Handle, authority)
	return projectResult(skillcmd.OperationUpdate, outcome), mapError(err)
}

// Change confirms and delegates enable, disable, or remove for one managed
// skill.
func (a *App) Change(ctx context.Context, operation skillcmd.Operation, selector string, confirm Confirm) (Result, error) {
	if operation != skillcmd.OperationEnable && operation != skillcmd.OperationDisable && operation != skillcmd.OperationRemove {
		return Result{}, newError(CodeInvalid)
	}
	authority := a.authority()
	inventory, entry, err := a.managed(ctx, authority, selector)
	if err != nil {
		return Result{}, err
	}
	review := Review{Operation: string(operation), Entry: projectEntry(entry), Impact: changeImpact(operation)}
	if err := approve(review, confirm); err != nil {
		return Result{}, err
	}
	outcome, err := a.lifecycle.Change(ctx, operation, mutation(inventory, entry, authority))
	return projectResult(operation, outcome), mapError(err)
}

// Refresh confirms and rebuilds eligible filesystem skill roots.
func (a *App) Refresh(ctx context.Context, confirm Confirm) (Result, error) {
	authority := a.authority()
	inventory, err := a.lifecycle.Inventory(ctx, authority)
	if err != nil {
		return Result{}, mapError(err)
	}
	review := Review{Operation: string(skillcmd.OperationRefresh), Impact: "rebuild eligible global skill roots"}
	if err := approve(review, confirm); err != nil {
		return Result{}, err
	}
	outcome, err := a.lifecycle.Refresh(ctx, skillcmd.Mutation{ExpectedState: inventory.ExpectedState, Authority: authority})
	return projectResult(skillcmd.OperationRefresh, outcome), mapError(err)
}

// Recover retries publication already durably accepted by an earlier command.
func (a *App) Recover(ctx context.Context) (Result, error) {
	outcome, err := a.lifecycle.RetryPublication(ctx, a.authority())
	return projectResult("recover", outcome), mapError(err)
}

func (a *App) inventory(ctx context.Context) (skillcmd.Inventory, error) {
	inventory, err := a.lifecycle.Inventory(ctx, a.authority())
	return inventory, mapError(err)
}

func (a *App) managed(ctx context.Context, authority skillcmd.Authority, selector string) (skillcmd.Inventory, skillcmd.Entry, error) {
	inventory, err := a.lifecycle.Inventory(ctx, authority)
	if err != nil {
		return skillcmd.Inventory{}, skillcmd.Entry{}, mapError(err)
	}
	entry, err := resolve(inventory.Entries, selector)
	if err != nil {
		return skillcmd.Inventory{}, skillcmd.Entry{}, err
	}
	if entry.Ownership != skillcmd.OwnershipManaged {
		return skillcmd.Inventory{}, skillcmd.Entry{}, newError(CodeNotOwned)
	}
	return inventory, entry, nil
}

func (a *App) authority() skillcmd.Authority { return skillcmd.NewCLIAuthority(a.now()) }

func mutation(inventory skillcmd.Inventory, entry skillcmd.Entry, authority skillcmd.Authority) skillcmd.Mutation {
	return skillcmd.Mutation{ID: entry.ID.Source, ExpectedState: inventory.ExpectedState, Authority: authority}
}

func approve(review Review, confirm Confirm) error {
	if confirm == nil {
		return newError(CodeConfirmationRequired)
	}
	ok, err := confirm(review)
	if err != nil {
		return newError(CodeConfirmationRequired)
	}
	if !ok {
		return newError(CodeCancelled)
	}
	return nil
}

func resolve(entries []skillcmd.Entry, selector string) (skillcmd.Entry, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return skillcmd.Entry{}, newError(CodeInvalid)
	}
	for _, entry := range entries {
		if publicID(entry) == selector {
			return entry, nil
		}
	}
	var matches []skillcmd.Entry
	for _, entry := range entries {
		if entry.Name == selector {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return skillcmd.Entry{}, newError(CodeNotFound)
	}
	candidates := make([]string, 0, len(matches))
	for _, entry := range matches {
		candidates = append(candidates, publicID(entry))
	}
	sort.Strings(candidates)
	return skillcmd.Entry{}, &Error{Code: CodeAmbiguous, Candidates: candidates}
}

func publicID(entry skillcmd.Entry) string {
	if entry.Ownership == skillcmd.OwnershipManaged {
		return entry.ID.Source.String()
	}
	return entry.ID.String()
}

func candidateReview(operation skillcmd.Operation, candidate skillcmd.Candidate) Review {
	return Review{
		Operation:      string(operation),
		Entry:          projectEntry(candidate.Entry),
		SkillPath:      candidate.SkillPath,
		ResolvedCommit: candidate.ResolvedCommit,
		Impact:         "accept reviewed skill revision",
	}
}

func changeImpact(operation skillcmd.Operation) string {
	switch operation {
	case skillcmd.OperationEnable:
		return "enable managed skill"
	case skillcmd.OperationDisable:
		return "disable managed skill"
	case skillcmd.OperationRemove:
		return "remove managed skill and retain history"
	default:
		return "change managed skill"
	}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return newError(CodeCancelled)
	case errors.Is(err, skillcmd.ErrInvalid):
		return newError(CodeInvalid)
	case errors.Is(err, skillcmd.ErrConflict):
		return newError(CodeConflict)
	case errors.Is(err, skillcmd.ErrForbidden):
		return newError(CodeNotOwned)
	case errors.Is(err, skillcmd.ErrNotFound):
		return newError(CodeNotFound)
	default:
		return newError(CodeUnavailable)
	}
}

// SourceKindGit is the canonical Git install kind understood by skillapp.
const SourceKindGit = "git"

// SourceKindLocal is the canonical local install kind understood by skillapp.
const SourceKindLocal = "local"

// InstallKind infers the existing skillapp source kind without retaining the
// source in any public result.
func InstallKind(source string) string {
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") || scpLikeSource(source) {
		return SourceKindGit
	}
	return SourceKindLocal
}

func scpLikeSource(source string) bool {
	host, path, ok := strings.Cut(source, ":")
	return ok && len(host) > 1 && path != "" && !strings.HasPrefix(path, ":") && !strings.ContainsAny(host, "/\\ \t")
}
