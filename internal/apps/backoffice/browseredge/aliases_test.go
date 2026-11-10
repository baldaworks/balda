package browseredge

import (
	"context"
	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestAliasBrowserCreateAndConflict(t *testing.T) {
	service := &aliasEdgeService{}
	app := newTestApp(t, service)
	create := mutationRequest("/balda/backoffice/aliases", url.Values{"csrf_token": {"csrf"}, "name": {"ops"}, "locator_ref": {"telegram:123"}})
	create.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, create)
	require.Equal(t, http.StatusSeeOther, created.Code)
	require.Equal(t, "ops", service.created.Name)
	service.retargetErr = managementclient.ErrConflict
	retarget := mutationRequest("/balda/backoffice/aliases/ops", url.Values{"csrf_token": {"csrf"}, "expected_version": {"1"}, "locator_ref": {"telegram:456"}})
	retarget.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	conflict := httptest.NewRecorder()
	app.Handler().ServeHTTP(conflict, retarget)
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Equal(t, uint64(1), service.retargeted.ExpectedVersion)
}

type aliasEdgeService struct {
	fakeService
	created     managementv1.AliasCreateRequest
	retargeted  managementv1.AliasRetargetRequest
	retargetErr error
}

func (s *aliasEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *aliasEdgeService) Aliases(context.Context, managementclient.Auth) (managementv1.AliasInventory, error) {
	return managementv1.AliasInventory{}, nil
}
func (s *aliasEdgeService) Alias(context.Context, managementclient.Auth, string) (managementv1.AliasRecord, error) {
	now := time.Now().UTC()
	return managementv1.AliasRecord{Name: "ops", LocatorRef: "telegram:123", Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (s *aliasEdgeService) CreateAlias(_ context.Context, _ managementclient.Auth, r managementv1.AliasCreateRequest) (managementv1.AliasRecord, error) {
	s.created = r
	now := time.Now().UTC()
	return managementv1.AliasRecord{Name: r.Name, LocatorRef: r.LocatorRef, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (s *aliasEdgeService) RetargetAlias(_ context.Context, _ managementclient.Auth, _ string, r managementv1.AliasRetargetRequest) (managementv1.AliasRecord, error) {
	s.retargeted = r
	return managementv1.AliasRecord{}, s.retargetErr
}
func (s *aliasEdgeService) DeleteAlias(context.Context, managementclient.Auth, string, managementv1.AliasDeleteRequest) error {
	return nil
}

var _ Service = (*aliasEdgeService)(nil)
var _ AliasService = (*aliasEdgeService)(nil)
