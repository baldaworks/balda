// Package clienttest provides a minimal client-neutral management API exercise
// for integration tests. It is not a production client or SDK.
package clienttest

import (
	"context"
	"fmt"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

// Input contains the values required by Exercise.
type Input struct {
	Username       string
	Password       string
	BrowserBinding string
	CSRFToken      string
	AliasName      string
	LocatorRef     string
}

// Observation contains the non-secret results required by integration tests.
type Observation struct {
	Compatibility managementv1.ManagementCompatibility
	Principal     managementv1.WebAuthPrincipal
	Inventory     managementv1.AliasInventory
	Created       managementv1.AliasRecord
}

// Exercise proves compatibility, WebAuth, a guarded read, and a CSRF-protected
// mutation using only the generated management client.
func Exercise(ctx context.Context, client *managementv1.ClientWithResponses, input Input) (Observation, error) {
	if client == nil {
		return Observation{}, fmt.Errorf("management client is required")
	}
	compatibility, err := client.GetManagementCompatibilityWithResponse(ctx)
	if err != nil || compatibility == nil || compatibility.JSON200 == nil {
		return Observation{}, fmt.Errorf("read management compatibility")
	}
	if !compatible(*compatibility.JSON200) {
		return Observation{}, fmt.Errorf("management API is incompatible")
	}

	password := input.Password
	binding := input.BrowserBinding
	csrf := input.CSRFToken
	login, err := client.LoginWebAuthWithResponse(ctx, managementv1.LocalLoginRequest{
		Username: input.Username,
		Password: &password,
		BrowserContext: managementv1.WebAuthBrowserContext{
			BrowserBinding: &binding,
			CSRFToken:      &csrf,
		},
	})
	if err != nil || login == nil || login.JSON200 == nil || login.JSON200.Credentials == nil {
		return Observation{}, fmt.Errorf("establish WebAuth")
	}
	credentials := *login.JSON200.Credentials
	read := credentialEditor(credentials.AccessToken, "")
	write := credentialEditor(credentials.AccessToken, credentials.CSRFToken)

	principal, err := client.GetWebAuthPrincipalWithResponse(ctx, read)
	if err != nil || principal == nil || principal.JSON200 == nil {
		return Observation{}, fmt.Errorf("read WebAuth principal")
	}
	inventory, err := client.ListAliasesWithResponse(ctx, read)
	if err != nil || inventory == nil || inventory.JSON200 == nil {
		return Observation{}, fmt.Errorf("read aliases")
	}
	created, err := client.CreateAliasWithResponse(ctx, managementv1.AliasCreateRequest{
		Name: input.AliasName, LocatorRef: input.LocatorRef,
	}, write)
	if err != nil || created == nil || created.JSON200 == nil {
		return Observation{}, fmt.Errorf("create alias")
	}

	return Observation{
		Compatibility: *compatibility.JSON200,
		Principal:     *principal.JSON200,
		Inventory:     *inventory.JSON200,
		Created:       *created.JSON200,
	}, nil
}

func compatible(value managementv1.ManagementCompatibility) bool {
	return value.ContractID == managementv1.BaldaManagementV1 && value.APIMajor == managementv1.N1
}

func credentialEditor(access, csrf string) managementv1.RequestEditorFn {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("X-Balda-WebAuth-Access", access)
		if csrf != "" {
			request.Header.Set("X-Balda-CSRF", csrf)
		}
		return nil
	}
}
