package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListAliases(ctx context.Context, _ managementv1.ListAliasesRequestObject) (managementv1.ListAliasesResponseObject, error) {
	records, err := s.aliases.List(ctx, aliasAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.AliasInventory{Items: make([]managementv1.AliasRecord, 0, len(records))}
	for _, record := range records {
		result.Items = append(result.Items, aliasRecordDTO(record))
	}
	return managementv1.ListAliases200JSONResponse(result), nil
}
func (s strictServer) GetAlias(ctx context.Context, request managementv1.GetAliasRequestObject) (managementv1.GetAliasResponseObject, error) {
	record, err := s.aliases.Get(ctx, request.AliasName, aliasAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetAlias200JSONResponse(aliasRecordDTO(record)), nil
}
func (s strictServer) CreateAlias(ctx context.Context, request managementv1.CreateAliasRequestObject) (managementv1.CreateAliasResponseObject, error) {
	if request.Body == nil {
		return nil, aliascmd.ErrInvalid
	}
	record, err := s.aliases.Create(ctx, aliascmd.Create{Name: request.Body.Name, LocatorRef: request.Body.LocatorRef, Authority: aliasAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateAlias200JSONResponse(aliasRecordDTO(record)), nil
}
func (s strictServer) RetargetAlias(ctx context.Context, request managementv1.RetargetAliasRequestObject) (managementv1.RetargetAliasResponseObject, error) {
	if request.Body == nil {
		return nil, aliascmd.ErrInvalid
	}
	record, err := s.aliases.Retarget(ctx, aliascmd.Retarget{Name: request.AliasName, LocatorRef: request.Body.LocatorRef, ExpectedVersion: request.Body.ExpectedVersion, Authority: aliasAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.RetargetAlias200JSONResponse(aliasRecordDTO(record)), nil
}
func (s strictServer) DeleteAlias(ctx context.Context, request managementv1.DeleteAliasRequestObject) (managementv1.DeleteAliasResponseObject, error) {
	if request.Body == nil {
		return nil, aliascmd.ErrInvalid
	}
	if err := s.aliases.Delete(ctx, aliascmd.Delete{Name: request.AliasName, ExpectedVersion: request.Body.ExpectedVersion, Authority: aliasAuthority(ctx)}); err != nil {
		return nil, err
	}
	return managementv1.DeleteAlias204Response{}, nil
}
func aliasAuthority(ctx context.Context) aliascmd.Authority {
	p := webAuthFromContext(ctx).principal
	return aliascmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version, MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}
func aliasRecordDTO(record aliascmd.Record) managementv1.AliasRecord {
	return managementv1.AliasRecord{Name: record.Name, LocatorRef: record.LocatorRef, Version: record.Version, CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC()}
}
