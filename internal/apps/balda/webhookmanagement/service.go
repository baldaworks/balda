// Package webhookmanagement owns durable webhook route management policy.
package webhookmanagement

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
	"text/template"

	"github.com/baldaworks/balda/internal/apps/balda/destinationcmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/google/uuid"
)

var routeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var aliasName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Store is the durable route and administrator-authority port used by policy.
type Store interface {
	Get(ctx context.Context, name string) (webhookroutecmd.Record, bool, error)
	List(ctx context.Context) ([]webhookroutecmd.Record, error)
	ReconcileConfig(ctx context.Context, routes []webhookroutecmd.Record) error
	CheckAuthority(ctx context.Context, authority webhookroutecmd.Authority) error
	Save(ctx context.Context, mutation webhookroutecmd.Mutation) error
}

// Service validates route changes and delegates versioned writes to Store.
type Service struct{ store Store }

// New composes webhook management with its durable store.
func New(store Store) *Service { return &Service{store: store} }

// Inventory returns current config and managed routes after checking authority.
func (s *Service) Inventory(ctx context.Context, authority webhookroutecmd.Authority) ([]webhookroutecmd.Item, error) {
	if err := s.checkAuthority(ctx, authority); err != nil {
		return nil, err
	}
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]webhookroutecmd.Item, 0, len(records))
	for _, r := range records {
		items = append(items, project(r))
	}
	return items, nil
}

// Get returns one route, including archived routes, after checking authority.
func (s *Service) Get(ctx context.Context, name string, authority webhookroutecmd.Authority) (webhookroutecmd.Item, error) {
	if err := s.checkAuthority(ctx, authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	r, err := s.get(ctx, name)
	if err != nil {
		return webhookroutecmd.Item{}, err
	}
	return project(r), nil
}

// ReconcileConfig saves non-secret metadata from raw config declarations.
// Disabled ingress historically ignored invalid declarations, so incomplete
// disabled placeholders are skipped until configuration enables them.
func (s *Service) ReconcileConfig(ctx context.Context, routes []webhookroutecmd.ConfiguredRoute) error {
	records := make([]webhookroutecmd.Record, 0, len(routes))
	for _, route := range routes {
		r, err := configuredRecord(route)
		if err != nil {
			if !route.Enabled {
				continue
			}
			return err
		}
		records = append(records, r)
	}
	return s.store.ReconcileConfig(ctx, records)
}

// Create persists a new managed route and returns its generated secret once.
func (s *Service) Create(ctx context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error) {
	if err := s.checkAuthority(ctx, request.Authority); err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	r, err := managedRecord(request.Definition)
	if err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	secret, verifier, err := newSecret()
	if err != nil {
		return webhookroutecmd.SecretResult{}, webhookroutecmd.ErrUnavailable
	}
	r.Source, r.Enabled, r.Version = webhookroutecmd.SourceManaged, true, 1
	r.AuthType, r.AuthHeader, r.SecretVerifier = webhookroutecmd.AuthTypeHeader, webhookroutecmd.ManagedSecretHeader, verifier
	r.CreatedAt, r.UpdatedAt = request.Authority.At, request.Authority.At
	if err := s.save(ctx, webhookroutecmd.MutationCreate, r, 0, request.Authority); err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	return webhookroutecmd.SecretResult{Item: project(r), Secret: secret}, nil
}

// Update replaces a managed route definition without changing its secret.
func (s *Service) Update(ctx context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error) {
	if err := s.checkAuthority(ctx, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	previous, err := s.getManaged(ctx, request.Name, request.ExpectedVersion)
	if err != nil {
		return webhookroutecmd.Item{}, err
	}
	if request.Definition.Name != request.Name {
		return webhookroutecmd.Item{}, webhookroutecmd.ErrInvalid
	}
	r, err := managedRecord(request.Definition)
	if err != nil {
		return webhookroutecmd.Item{}, err
	}
	r.Source, r.Enabled, r.Version = webhookroutecmd.SourceManaged, previous.Enabled, previous.Version+1
	r.AuthType, r.AuthHeader = webhookroutecmd.AuthTypeHeader, webhookroutecmd.ManagedSecretHeader
	r.CreatedAt, r.UpdatedAt = previous.CreatedAt, request.Authority.At
	if err := s.save(ctx, webhookroutecmd.MutationEdit, r, request.ExpectedVersion, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	return project(r), nil
}

// SetEnabled changes whether future HTTP requests can enter a managed route.
func (s *Service) SetEnabled(ctx context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error) {
	if err := s.checkAuthority(ctx, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	r, err := s.getManaged(ctx, request.Name, request.ExpectedVersion)
	if err != nil {
		return webhookroutecmd.Item{}, err
	}
	r.Enabled, r.Version = request.Enabled, r.Version+1
	r.UpdatedAt = request.Authority.At
	if err := s.save(ctx, webhookroutecmd.MutationSelection, r, request.ExpectedVersion, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	return project(r), nil
}

// Delete archives a managed route and retains its name and request history.
func (s *Service) Delete(ctx context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error) {
	if err := s.checkAuthority(ctx, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	r, err := s.getManaged(ctx, request.Name, request.ExpectedVersion)
	if err != nil {
		return webhookroutecmd.Item{}, err
	}
	r.Enabled, r.Deleted, r.Version = false, true, r.Version+1
	r.UpdatedAt = request.Authority.At
	if err := s.save(ctx, webhookroutecmd.MutationDelete, r, request.ExpectedVersion, request.Authority); err != nil {
		return webhookroutecmd.Item{}, err
	}
	return project(r), nil
}

// Rotate atomically replaces a managed route's verifier and returns the new secret once.
func (s *Service) Rotate(ctx context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error) {
	if err := s.checkAuthority(ctx, request.Authority); err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	r, err := s.getManaged(ctx, request.Name, request.ExpectedVersion)
	if err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	secret, verifier, err := newSecret()
	if err != nil {
		return webhookroutecmd.SecretResult{}, webhookroutecmd.ErrUnavailable
	}
	r.SecretVerifier, r.Version = verifier, r.Version+1
	r.UpdatedAt = request.Authority.At
	if err := s.save(ctx, webhookroutecmd.MutationRotate, r, request.ExpectedVersion, request.Authority); err != nil {
		return webhookroutecmd.SecretResult{}, err
	}
	return webhookroutecmd.SecretResult{Item: project(r), Secret: secret}, nil
}

func (s *Service) getManaged(ctx context.Context, name string, version uint64) (webhookroutecmd.Record, error) {
	r, err := s.get(ctx, name)
	if err != nil {
		return r, err
	}
	if r.Deleted {
		return r, webhookroutecmd.ErrNotFound
	}
	if r.Source != webhookroutecmd.SourceManaged {
		return r, webhookroutecmd.ErrForbidden
	}
	if version == 0 || version != r.Version || version >= math.MaxInt64 {
		return r, webhookroutecmd.ErrConflict
	}
	return r, nil
}

func (s *Service) get(ctx context.Context, name string) (webhookroutecmd.Record, error) {
	r, found, err := s.store.Get(ctx, name)
	if err != nil {
		return webhookroutecmd.Record{}, err
	}
	if !found {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrNotFound
	}
	return r, nil
}

func (s *Service) checkAuthority(ctx context.Context, authority webhookroutecmd.Authority) error {
	return s.store.CheckAuthority(ctx, authority)
}

func (s *Service) save(ctx context.Context, kind webhookroutecmd.MutationKind, r webhookroutecmd.Record,
	version uint64, authority webhookroutecmd.Authority) error {
	audit := usercmd.AuditEvent{ID: uuid.NewString(), Action: usercmd.AuditActionWebhookRouteChanged,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.UserID,
		ActorSessionID: authority.SessionID, TargetType: usercmd.AuditTargetWebhookRoute,
		TargetID: r.Name, Source: "backoffice", OccurredAt: authority.At}
	return s.store.Save(ctx, webhookroutecmd.Mutation{Kind: kind, Record: r,
		ExpectedVersion: version, Authority: authority, Audit: audit})
}

func newSecret() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(secret))
	return secret, hex.EncodeToString(digest[:]), nil
}

func project(r webhookroutecmd.Record) webhookroutecmd.Item {
	reportTo := r.ReportToKey
	return webhookroutecmd.Item{Definition: webhookroutecmd.Definition{
		Name: r.Name, Path: r.Path, PromptTemplate: r.PromptTemplate,
		ReportTo: reportTo, AckOnDelivery: r.AckOnDelivery,
		DedupeSource: r.DedupeSource, DedupeHeader: r.DedupeHeader},
		Source: r.Source, Enabled: r.Enabled, Deleted: r.Deleted, Version: r.Version,
		ReportToKind: r.ReportToKind, ReportToKey: r.ReportToKey,
		AuthType: r.AuthType, AuthHeader: r.AuthHeader,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func managedRecord(d webhookroutecmd.Definition) (webhookroutecmd.Record, error) {
	name := strings.TrimSpace(d.Name)
	if name != d.Name || !routeName.MatchString(name) {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	r := webhookroutecmd.Record{Name: name, Path: strings.TrimSpace(d.Path),
		PromptTemplate: strings.TrimSpace(d.PromptTemplate), AckOnDelivery: d.AckOnDelivery}
	if err := validateManagedBody(r.Path, r.PromptTemplate); err != nil {
		return webhookroutecmd.Record{}, err
	}
	if d.ReportTo != "" {
		ref := strings.TrimSpace(d.ReportTo)
		if ref != d.ReportTo {
			return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
		}
		if locator, err := locatorref.Parse(ref); err == nil {
			r.ReportToKind, r.ReportToKey = destinationcmd.TargetLocator, locatorref.Format(locator)
		} else if aliasName.MatchString(ref) && ref != "owner" && ref != "collaborator" {
			r.ReportToKind, r.ReportToKey = destinationcmd.TargetManagedAlias, ref
		} else {
			return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
		}
	}
	if r.AckOnDelivery && r.ReportToKey == "" {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	r.DedupeSource, r.DedupeHeader = normalizedDedupe(d.DedupeSource, d.DedupeHeader)
	if !validDedupe(r.DedupeSource, r.DedupeHeader) ||
		strings.EqualFold(r.DedupeHeader, webhookroutecmd.ManagedSecretHeader) {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	return r, nil
}

func configuredRecord(route webhookroutecmd.ConfiguredRoute) (webhookroutecmd.Record, error) {
	name := strings.TrimSpace(route.Name)
	if name == "" || name != route.Name {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	r := webhookroutecmd.Record{Name: name, Source: webhookroutecmd.SourceConfig,
		Path: strings.TrimSpace(route.Path), PromptTemplate: strings.TrimSpace(route.PromptTemplate),
		ReportToKind: strings.TrimSpace(route.ReportToKind), ReportToKey: strings.TrimSpace(route.ReportToKey),
		AckOnDelivery: route.AckOnDelivery, Enabled: route.Enabled,
		AuthType: strings.TrimSpace(route.AuthType), AuthHeader: strings.TrimSpace(route.AuthHeader)}
	if err := validateConfiguredBody(r.Path, r.PromptTemplate); err != nil {
		return webhookroutecmd.Record{}, err
	}
	if r.ReportToKind == "" && r.ReportToKey != "" || r.ReportToKind != "" && r.ReportToKey == "" {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	switch r.ReportToKind {
	case "":
	case destinationcmd.TargetLocator:
		if _, err := locatorref.Parse(r.ReportToKey); err != nil {
			return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
		}
	case destinationcmd.TargetManagedAlias, destinationcmd.TargetAlias:
	default:
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	if r.AckOnDelivery && r.ReportToKey == "" {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	r.DedupeSource, r.DedupeHeader = normalizedDedupe(route.DedupeSource, route.DedupeHeader)
	if !validConfiguredDedupe(r.DedupeSource, r.DedupeHeader) {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	if r.AuthType == "" {
		r.AuthType = webhookroutecmd.AuthTypeNone
	}
	if r.AuthType != webhookroutecmd.AuthTypeNone && r.AuthType != webhookroutecmd.AuthTypeHeader ||
		r.AuthType == webhookroutecmd.AuthTypeHeader && r.AuthHeader == "" {
		return webhookroutecmd.Record{}, webhookroutecmd.ErrInvalid
	}
	if r.AuthType == webhookroutecmd.AuthTypeNone {
		r.AuthHeader = ""
	}
	return r, nil
}

func validateManagedBody(path, prompt string) error {
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t\r\n?#") ||
		prompt == "" || len(prompt) > 16384 {
		return webhookroutecmd.ErrInvalid
	}
	return validateTemplate(prompt)
}

func validateConfiguredBody(path, prompt string) error {
	if path == "" || !strings.HasPrefix(path, "/") || prompt == "" {
		return webhookroutecmd.ErrInvalid
	}
	return validateTemplate(prompt)
}

func validateTemplate(prompt string) error {
	if _, err := template.New("webhook").Option("missingkey=error").Parse(prompt); err != nil {
		return webhookroutecmd.ErrInvalid
	}
	return nil
}

func normalizedDedupe(source, header string) (string, string) {
	source, header = strings.ToLower(strings.TrimSpace(source)), strings.TrimSpace(header)
	if source == "" {
		if header != "" {
			source = webhookroutecmd.DedupeSourceHeader
		} else {
			source = webhookroutecmd.DedupeSourceRequestID
		}
	}
	return source, header
}

func validDedupe(source, header string) bool {
	switch source {
	case webhookroutecmd.DedupeSourceRequestID, webhookroutecmd.DedupeSourceBodySHA:
		return header == ""
	case webhookroutecmd.DedupeSourceHeader:
		return header != "" && !strings.ContainsAny(header, " \t\r\n:")
	default:
		return false
	}
}

func validConfiguredDedupe(source, header string) bool {
	switch source {
	case webhookroutecmd.DedupeSourceRequestID, webhookroutecmd.DedupeSourceBodySHA:
		return true
	case webhookroutecmd.DedupeSourceHeader:
		return header != ""
	default:
		return false
	}
}
