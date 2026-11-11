package managementhttp

import (
	"context"
	"strconv"

	"github.com/baldaworks/balda/internal/apps/balda/auditapp"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListAuditEvents(ctx context.Context, request managementv1.ListAuditEventsRequestObject) (managementv1.ListAuditEventsResponseObject, error) {
	params := request.Params
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	query := auditapp.Request{
		AfterID: pointerValue(params.AfterID), Limit: limit,
		Action: usercmd.AuditAction(pointerValue(params.Action)), Outcome: usercmd.AuditOutcome(pointerValue(params.Outcome)),
		TargetType: usercmd.AuditTargetType(pointerValue(params.TargetType)), ActorUserID: pointerValue(params.ActorUserID),
	}
	if params.From != nil {
		query.From = params.From.UTC()
	}
	if params.Before != nil {
		query.Before = params.Before.UTC()
	}
	page, err := s.audit.List(ctx, webAuthFromContext(ctx).principal.User, query)
	if err != nil {
		return nil, err
	}
	result := managementv1.AuditEventPage{Events: make([]managementv1.AuditEvent, 0, len(page.Events))}
	for _, event := range page.Events {
		result.Events = append(result.Events, auditEventDTO(event))
	}
	if page.NextAfterID != "" {
		result.NextAfterID = &page.NextAfterID
	}
	return managementv1.ListAuditEvents200JSONResponse(result), nil
}

func auditEventDTO(event usercmd.AuditEvent) managementv1.AuditEvent {
	result := managementv1.AuditEvent{
		EventID: safeAuditID(event.ID), Action: string(event.Action), Outcome: string(event.Outcome),
		TargetType: string(event.TargetType), OccurredAt: event.OccurredAt.UTC(),
	}
	if event.ActorUserID != "" {
		value := safeAuditID(event.ActorUserID)
		result.ActorUserID = &value
	}
	if event.ActorWebAuthSessionID != "" {
		value := safeAuditID(event.ActorWebAuthSessionID)
		result.ActorWebAuthSessionID = &value
	}
	if event.TargetID != "" {
		value := safeAuditTargetID(event.TargetType, event.TargetID)
		result.TargetID = &value
	}
	return result
}

const redactedAuditID = "[redacted]"

func safeAuditID(value string) string {
	if value == "" {
		return ""
	}
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return redactedAuditID
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return redactedAuditID
		}
	}
	return value
}

func safeAuditTargetID(target usercmd.AuditTargetType, value string) string {
	if target == usercmd.AuditTargetMemoryFact {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
			return redactedAuditID
		}
		return value
	}
	if target != usercmd.AuditTargetAlias {
		return safeAuditID(value)
	}
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return redactedAuditID
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return redactedAuditID
		}
	}
	return value
}
