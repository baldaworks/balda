package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListAISessionArchives(ctx context.Context, request managementv1.ListAISessionArchivesRequestObject) (managementv1.ListAISessionArchivesResponseObject, error) {
	query := chatarchivecmd.BrowseQuery{}
	if request.Params.Q != nil {
		query.Query = *request.Params.Q
	}
	if request.Params.Channel != nil {
		query.Channel = *request.Params.Channel
	}
	if request.Params.Status != nil {
		query.Status = string(*request.Params.Status)
	}
	if request.Params.Cursor != nil {
		query.Cursor = *request.Params.Cursor
	}
	inventory, err := s.archives.Inventory(ctx, query, archiveAuthority(ctx))
	if err != nil {
		return nil, err
	}
	rows := make([]managementv1.AISessionArchiveSummary, 0, len(inventory.Rows))
	for _, row := range inventory.Rows {
		mapped, mapErr := archiveSummaryDTO(row)
		if mapErr != nil {
			return nil, mapErr
		}
		rows = append(rows, mapped)
	}
	return managementv1.ListAISessionArchives200JSONResponse{Rows: rows, OlderCursor: inventory.Older}, nil
}

func (s strictServer) GetAISessionArchive(ctx context.Context, request managementv1.GetAISessionArchiveRequestObject) (managementv1.GetAISessionArchiveResponseObject, error) {
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	transcript, err := s.archives.Transcript(ctx, request.AISessionID, cursor, archiveAuthority(ctx))
	if err != nil {
		return nil, err
	}
	summary, err := archiveSummaryDTO(transcript.Summary)
	if err != nil {
		return nil, err
	}
	messages := make([]managementv1.AISessionArchiveMessage, 0, len(transcript.Messages))
	for _, message := range transcript.Messages {
		if message.Role != "user" && message.Role != "agent" {
			continue
		}
		attachments := make([]managementv1.AISessionArchiveAttachment, 0, len(message.Attachments))
		for _, attachment := range message.Attachments {
			attachments = append(attachments, managementv1.AISessionArchiveAttachment{Name: attachment.Name, Kind: attachment.Kind})
		}
		mapped := managementv1.AISessionArchiveMessage{Sequence: message.Sequence, Role: managementv1.AISessionArchiveMessageRole(message.Role), Text: message.Text, Speaker: archiveSpeakerDTO(message.Speaker), Attachments: attachments}
		mapped.ObservedAt = timePointer(message.ObservedAt)
		messages = append(messages, mapped)
	}
	return managementv1.GetAISessionArchive200JSONResponse{Summary: summary, Messages: messages, OlderCursor: transcript.Older, SuccessorAISessionID: transcript.SuccessorID}, nil
}

func archiveSummaryDTO(summary chatarchivecmd.Summary) (managementv1.AISessionArchiveSummary, error) {
	if summary.Session.ID == "" {
		return managementv1.AISessionArchiveSummary{}, chatarchivecmd.ErrInvalid
	}
	session := summary.Session
	return managementv1.AISessionArchiveSummary{AISessionID: session.ID, Label: session.Label, Channel: session.Location.Channel, Locator: session.Location.Locator,
		Title: session.Location.Title, Audience: session.Location.Audience, Subdivision: session.Location.Subdivision, SubdivisionLabel: session.Location.SubdivisionLabel,
		Status: session.Status, StartedAt: timePointer(session.StartedAt), ObservedAt: timePointer(session.ObservedAt), EndedAt: timePointer(session.EndedAt),
		CoverageStart: timePointer(session.CoverageStart), EndReason: session.EndReason, Partial: session.Partial, PredecessorAISessionID: session.PredecessorID,
		MessageCount: summary.MessageCount, Speaker: archiveSpeakerDTO(summary.Speaker)}, nil
}

func archiveSpeakerDTO(speaker chatarchivecmd.Speaker) managementv1.AISessionArchiveSpeaker {
	return managementv1.AISessionArchiveSpeaker{Principal: speaker.Principal, Name: speaker.Name, Username: speaker.Username}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func archiveAuthority(ctx context.Context) chatarchivecmd.Authority {
	p := webAuthFromContext(ctx).principal
	return chatarchivecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}
