package mcpbackofficeapp

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
)

// ConfigureAuthorizations binds the existing protocol owner before serving HTTP.
func (o *Operations) ConfigureAuthorizations(authorizations *mcpmanage.Authorizations) error {
	if authorizations == nil {
		return mcpcmd.ErrInvalid
	}
	o.authorizations = authorizations
	return nil
}

// CreateAndBeginBrowser keeps the saved identity available if protocol start
// fails. Only the trusted creation authority can bind the new attempt.
func (o *Operations) CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error) {
	item, request, err := o.createAuthorization(ctx, creation, request)
	if err != nil {
		return item, mcpcmd.BrowserAuthorization{}, err
	}
	started, err := o.BeginBrowser(ctx, request)
	return item, started, err
}

// CreateAndBeginDevice creates once, then starts the existing native device flow.
func (o *Operations) CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error) {
	item, request, err := o.createAuthorization(ctx, creation, request)
	if err != nil {
		return item, mcpcmd.DeviceAuthorization{}, err
	}
	started, err := o.BeginDevice(ctx, request)
	return item, started, err
}

func (o *Operations) createAuthorization(ctx context.Context, creation mcpcmd.CreateDefinition, request mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BeginAuthorization, error) {
	if o.authorizations == nil {
		return mcpcmd.Item{}, request, mcpcmd.ErrUnavailable
	}
	item, err := o.definitions.CreateForAuthorization(ctx, creation, request.Scopes)
	if err != nil {
		return item, request, err
	}
	request.ConnectionID, request.Authority = item.Connection.ID, creation.Authority
	return item, request, nil
}

// BeginBrowser captures trusted current file values before starting protocol I/O.
func (o *Operations) BeginBrowser(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error) {
	if o.authorizations == nil {
		return mcpcmd.BrowserAuthorization{}, mcpcmd.ErrUnavailable
	}
	revision, err := o.definitions.PrepareAuthorization(ctx, mcpcmd.PrepareAuthorization{ConnectionID: request.ConnectionID, Scopes: request.Scopes, Authority: request.Authority})
	if err != nil {
		return mcpcmd.BrowserAuthorization{}, err
	}
	return o.authorizations.BeginBrowser(ctx, revision, "", authorizationClient(request), request.Authority)
}

// BeginDevice captures trusted current file values before starting owned polling.
func (o *Operations) BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error) {
	if o.authorizations == nil {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrUnavailable
	}
	revision, err := o.definitions.PrepareAuthorization(ctx, mcpcmd.PrepareAuthorization{ConnectionID: request.ConnectionID, Scopes: request.Scopes, Authority: request.Authority})
	if err != nil {
		return mcpcmd.DeviceAuthorization{}, err
	}
	return o.authorizations.BeginDevice(ctx, revision, "", authorizationClient(request), request.Authority)
}

func authorizationClient(request mcpcmd.BeginAuthorization) mcpmanage.OAuthClient {
	return mcpmanage.OAuthClient{ID: request.ClientID, Secret: request.ClientSecret, AuthMethod: request.ClientAuthMethod}
}

// CompleteBrowser projects saved worker authorization separately from readiness.
func (o *Operations) CompleteBrowser(ctx context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Item, error) {
	if o.authorizations == nil {
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}
	grant, completionErr := o.authorizations.CompleteBrowser(ctx, callback)
	if grant.Binding.ConnectionID == "" {
		return mcpcmd.Item{}, completionErr
	}
	item, err := o.authorizationItem(ctx, grant.Binding.ConnectionID)
	if completionErr != nil {
		return item, completionErr
	}
	return item, err
}

// Device reads browser-bound transient progress without changing readiness.
func (o *Operations) Device(ctx context.Context, id string, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error) {
	if o.authorizations == nil {
		return mcpcmd.DeviceAuthorization{}, mcpcmd.ErrUnavailable
	}
	return o.authorizations.Device(ctx, id, authority)
}

// CurrentAttempt resolves configured public aliases without capturing file values.
func (o *Operations) CurrentAttempt(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error) {
	if o.authorizations == nil {
		return mcpcmd.AuthorizationAttempt{}, false, mcpcmd.ErrUnavailable
	}
	item, err := o.authorizationItem(ctx, connectionID)
	if err != nil {
		return mcpcmd.AuthorizationAttempt{}, false, err
	}
	return o.authorizations.CurrentAttempt(ctx, item.Connection.ID, authority)
}

// Cancel delegates cancellation to the one transient protocol owner.
func (o *Operations) Cancel(ctx context.Context, id string, authority mcpcmd.Authority) error {
	if o.authorizations == nil {
		return mcpcmd.ErrUnavailable
	}
	return o.authorizations.Cancel(ctx, id, authority)
}

// Disconnect revokes every retained grant context for this worker connection.
func (o *Operations) Disconnect(ctx context.Context, connectionID string, authority mcpcmd.Authority) error {
	if o.authorizations == nil {
		return mcpcmd.ErrUnavailable
	}
	item, err := o.authorizationItem(ctx, connectionID)
	if err != nil {
		return err
	}
	return o.authorizations.Disconnect(ctx, item.Connection.ID, authority)
}

// RetryAuthorization uses the saved exact current binding, never a submitted one.
func (o *Operations) RetryAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	item, err := o.authorizationItem(ctx, request.ConnectionID)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	if request.ExpectedRevisionID == "" || request.ExpectedRevisionID != item.Connection.CurrentRevisionID {
		return item, mcpcmd.ErrConflict
	}
	if item.Authorization == mcpcmd.GrantDisconnected {
		return item, mcpcmd.ErrDisconnected
	}
	if item.Authorization != mcpcmd.GrantAuthorized || item.Definition.AuthBinding == nil {
		return item, mcpcmd.ErrAuthRequired
	}
	request.ConnectionID, request.Binding = item.Connection.ID, *item.Definition.AuthBinding
	_, retryErr := o.definitions.BindAuthorization(ctx, request)
	item, err = o.authorizationItem(ctx, request.ConnectionID)
	if retryErr != nil {
		return item, retryErr
	}
	return item, err
}

func (o *Operations) authorizationItem(ctx context.Context, connectionID string) (mcpcmd.Item, error) {
	items, err := o.Inventory(ctx)
	if err != nil {
		return mcpcmd.Item{}, err
	}
	for _, item := range items {
		if item.Connection.ID == connectionID || (item.Connection.Source == mcpcmd.SourceConfig && "config:"+item.Connection.PublicID == connectionID && strings.HasPrefix(connectionID, "config:")) {
			return item, nil
		}
	}
	return mcpcmd.Item{}, mcpcmd.ErrNotFound
}
