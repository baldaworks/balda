package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const webhookHistoryPageSize = 20

func (s strictServer) ListWebhooks(ctx context.Context, _ managementv1.ListWebhooksRequestObject) (managementv1.ListWebhooksResponseObject, error) {
	items, err := s.webhooks.Inventory(ctx, webhookAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.WebhookInventory{Items: make([]managementv1.WebhookItem, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, webhookItemDTO(item))
	}
	return managementv1.ListWebhooks200JSONResponse(result), nil
}

func (s strictServer) GetWebhook(ctx context.Context, request managementv1.GetWebhookRequestObject) (managementv1.GetWebhookResponseObject, error) {
	item, err := s.webhooks.Get(ctx, request.WebhookName, webhookAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetWebhook200JSONResponse(webhookItemDTO(item)), nil
}

func (s strictServer) CreateWebhook(ctx context.Context, request managementv1.CreateWebhookRequestObject) (managementv1.CreateWebhookResponseObject, error) {
	if request.Body == nil {
		return nil, webhookroutecmd.ErrInvalid
	}
	result, err := s.webhooks.Create(ctx, webhookroutecmd.Create{Definition: webhookDefinition(request.Body.Definition), Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateWebhook200JSONResponse(managementv1.WebhookSecretResult{Item: webhookItemDTO(result.Item), Secret: result.Secret}), nil
}

func (s strictServer) UpdateWebhook(ctx context.Context, request managementv1.UpdateWebhookRequestObject) (managementv1.UpdateWebhookResponseObject, error) {
	if request.Body == nil || request.Body.Definition.Name != request.WebhookName {
		return nil, webhookroutecmd.ErrInvalid
	}
	item, err := s.webhooks.Update(ctx, webhookroutecmd.Update{Name: request.WebhookName, ExpectedVersion: request.Body.ExpectedVersion,
		Definition: webhookDefinition(request.Body.Definition), Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateWebhook200JSONResponse(webhookItemDTO(item)), nil
}

func (s strictServer) SelectWebhook(ctx context.Context, request managementv1.SelectWebhookRequestObject) (managementv1.SelectWebhookResponseObject, error) {
	if request.Body == nil {
		return nil, webhookroutecmd.ErrInvalid
	}
	item, err := s.webhooks.SetEnabled(ctx, webhookroutecmd.ChangeSelection{Name: request.WebhookName, ExpectedVersion: request.Body.ExpectedVersion,
		Enabled: request.Body.Enabled, Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.SelectWebhook200JSONResponse(webhookItemDTO(item)), nil
}

func (s strictServer) DeleteWebhook(ctx context.Context, request managementv1.DeleteWebhookRequestObject) (managementv1.DeleteWebhookResponseObject, error) {
	if request.Body == nil {
		return nil, webhookroutecmd.ErrInvalid
	}
	item, err := s.webhooks.Delete(ctx, webhookroutecmd.Delete{Name: request.WebhookName, ExpectedVersion: request.Body.ExpectedVersion, Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.DeleteWebhook200JSONResponse(webhookItemDTO(item)), nil
}

func (s strictServer) RotateWebhookSecret(ctx context.Context, request managementv1.RotateWebhookSecretRequestObject) (managementv1.RotateWebhookSecretResponseObject, error) {
	if request.Body == nil {
		return nil, webhookroutecmd.ErrInvalid
	}
	result, err := s.webhooks.Rotate(ctx, webhookroutecmd.Rotate{Name: request.WebhookName, ExpectedVersion: request.Body.ExpectedVersion, Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.RotateWebhookSecret200JSONResponse(managementv1.WebhookSecretResult{Item: webhookItemDTO(result.Item), Secret: result.Secret}), nil
}

func (s strictServer) TestWebhook(ctx context.Context, request managementv1.TestWebhookRequestObject) (managementv1.TestWebhookResponseObject, error) {
	if request.Body == nil {
		return nil, webhookroutecmd.ErrInvalid
	}
	result, err := s.webhooks.TestPost(ctx, webhookroutecmd.TestPost{Name: request.WebhookName, Body: request.Body.Body,
		RequestKey: request.Body.RequestKey.String(), ExpectedVersion: request.Body.ExpectedVersion,
		ConfirmDisabled: request.Body.ConfirmDisabled, Authority: webhookAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.TestWebhook200JSONResponse(managementv1.WebhookTestResult{JobID: result.JobID, Duplicate: result.Duplicate}), nil
}

func (s strictServer) ListWebhookHistory(ctx context.Context, request managementv1.ListWebhookHistoryRequestObject) (managementv1.ListWebhookHistoryResponseObject, error) {
	if (request.Params.BeforeAt == nil) != (request.Params.BeforeJobID == nil) {
		return nil, webhookroutecmd.ErrInvalid
	}
	var beforeAt time.Time
	var beforeJobID string
	if request.Params.BeforeAt != nil {
		beforeAt, beforeJobID = request.Params.BeforeAt.UTC(), *request.Params.BeforeJobID
	}
	items, err := s.webhooks.History(ctx, request.WebhookName, beforeAt, beforeJobID, webhookHistoryPageSize+1, webhookAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.WebhookHistoryPage{Items: make([]managementv1.WebhookHistorySummary, 0, min(len(items), webhookHistoryPageSize))}
	for _, item := range items[:min(len(items), webhookHistoryPageSize)] {
		result.Items = append(result.Items, webhookHistorySummaryDTO(item))
	}
	if len(items) > webhookHistoryPageSize {
		last := items[webhookHistoryPageSize-1]
		result.Next = &managementv1.WebhookHistoryCursor{BeforeAt: last.CreatedAt.UTC(), BeforeJobID: last.JobID}
	}
	return managementv1.ListWebhookHistory200JSONResponse(result), nil
}

func (s strictServer) GetWebhookHistory(ctx context.Context, request managementv1.GetWebhookHistoryRequestObject) (managementv1.GetWebhookHistoryResponseObject, error) {
	item, err := s.webhooks.HistoryDetail(ctx, request.WebhookName, request.JobID, webhookAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetWebhookHistory200JSONResponse(managementv1.WebhookHistoryDetail{Summary: webhookHistorySummaryDTO(item),
		InputAvailable: item.InputAvailable, Input: item.Input, Output: item.Output, DeliveryPayload: item.DeliveryPayload}), nil
}

func webhookAuthority(ctx context.Context) webhookroutecmd.Authority {
	p := webAuthFromContext(ctx).principal
	return webhookroutecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}

func webhookDefinition(value managementv1.WebhookDefinition) webhookroutecmd.Definition {
	return webhookroutecmd.Definition{Name: value.Name, PromptTemplate: value.PromptTemplate, ReportTo: value.ReportTo,
		AckOnDelivery: value.AckOnDelivery, DedupeSource: string(value.DedupeSource), DedupeHeader: value.DedupeHeader}
}

func webhookItemDTO(item webhookroutecmd.Item) managementv1.WebhookItem {
	return managementv1.WebhookItem{Definition: managementv1.WebhookDefinition{Name: item.Definition.Name, PromptTemplate: item.Definition.PromptTemplate,
		ReportTo: item.Definition.ReportTo, AckOnDelivery: item.Definition.AckOnDelivery,
		DedupeSource: managementv1.WebhookDefinitionDedupeSource(item.Definition.DedupeSource), DedupeHeader: item.Definition.DedupeHeader},
		Source: managementv1.WebhookItemSource(item.Source), Enabled: item.Enabled, Deleted: item.Deleted, Version: item.Version,
		ReportToKind: item.ReportToKind, ReportToKey: item.ReportToKey, AuthType: managementv1.WebhookItemAuthType(item.AuthType),
		AuthHeader: item.AuthHeader, CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC()}
}

func webhookHistorySummaryDTO(item webhookroutecmd.HistoryItem) managementv1.WebhookHistorySummary {
	return managementv1.WebhookHistorySummary{JobID: item.JobID, Source: item.Source, CreatedAt: item.CreatedAt.UTC(), JobStatus: item.JobStatus,
		DeliveryStatus: item.DeliveryStatus, HasReportTo: item.HasReportTo, ReportTo: item.ReportTo}
}
