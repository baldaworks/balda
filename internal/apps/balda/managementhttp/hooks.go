package managementhttp

import (
	"context"
	"math"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListHooks(ctx context.Context, _ managementv1.ListHooksRequestObject) (managementv1.ListHooksResponseObject, error) {
	record, err := s.hooks.Inventory(ctx, hookAuthority(ctx))
	if err != nil {
		return nil, err
	}
	definitions := make([]managementv1.HookDefinition, 0, len(record.Definitions))
	for _, definition := range record.Definitions {
		definitions = append(definitions, hookDefinitionDTO(definition))
	}
	return managementv1.ListHooks200JSONResponse{
		Version: record.Version, UpdatedAt: record.UpdatedAt, Definitions: definitions,
		Capabilities: s.hooks.CapabilityDescription(),
	}, nil
}

func (s strictServer) CreateHook(ctx context.Context, request managementv1.CreateHookRequestObject) (managementv1.CreateHookResponseObject, error) {
	if request.Body == nil {
		return nil, hookcmd.ErrInvalid
	}
	definition, err := hookDefinition(*request.Body)
	if err != nil {
		return nil, err
	}
	if err := s.hooks.Save(ctx, hookcmd.Save{Definition: definition, Create: true, ExpectedVersion: request.Body.ExpectedVersion, Authority: hookAuthority(ctx)}); err != nil {
		return nil, err
	}
	return managementv1.CreateHook204Response{}, nil
}

func (s strictServer) UpdateHook(ctx context.Context, request managementv1.UpdateHookRequestObject) (managementv1.UpdateHookResponseObject, error) {
	if request.Body == nil || request.HookName != request.Body.Definition.Name {
		return nil, hookcmd.ErrInvalid
	}
	definition, err := hookDefinition(*request.Body)
	if err != nil {
		return nil, err
	}
	if err := s.hooks.Save(ctx, hookcmd.Save{Definition: definition, ExpectedVersion: request.Body.ExpectedVersion, Authority: hookAuthority(ctx)}); err != nil {
		return nil, err
	}
	return managementv1.UpdateHook204Response{}, nil
}

func (s strictServer) DeleteHook(ctx context.Context, request managementv1.DeleteHookRequestObject) (managementv1.DeleteHookResponseObject, error) {
	if request.Body == nil || !request.Body.Confirm {
		return nil, hookcmd.ErrInvalid
	}
	if err := s.hooks.Delete(ctx, hookcmd.Delete{Name: request.HookName, ExpectedVersion: request.Body.ExpectedVersion, Authority: hookAuthority(ctx)}); err != nil {
		return nil, err
	}
	return managementv1.DeleteHook204Response{}, nil
}

func hookDefinition(request managementv1.HookSaveRequest) (hookcmd.Definition, error) {
	if request.Definition.Args == nil || request.Definition.TimeoutSeconds > int64(math.MaxInt) {
		return hookcmd.Definition{}, hookcmd.ErrInvalid
	}
	definition := hookcmd.Definition{
		Name: request.Definition.Name, Event: hookcmd.EventName(request.Definition.Event), Enabled: request.Definition.Enabled,
		Command: request.Definition.Command, Args: append([]string{}, request.Definition.Args...), Provider: request.Definition.Provider,
		Matcher: request.Definition.Matcher, TimeoutSeconds: int(request.Definition.TimeoutSeconds),
	}
	if definition.Validate() != nil {
		return hookcmd.Definition{}, hookcmd.ErrInvalid
	}
	return definition, nil
}

func hookDefinitionDTO(definition hookcmd.Definition) managementv1.HookDefinition {
	return managementv1.HookDefinition{
		Name: definition.Name, Event: managementv1.HookEvent(definition.Event), Enabled: definition.Enabled,
		Command: definition.Command, Args: append([]string{}, definition.Args...), Provider: definition.Provider,
		Matcher: definition.Matcher, TimeoutSeconds: int64(definition.TimeoutSeconds),
	}
}

func hookAuthority(ctx context.Context) hookcmd.Authority {
	p := webAuthFromContext(ctx).principal
	return hookcmd.Authority{
		UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID,
		WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC(),
	}
}
