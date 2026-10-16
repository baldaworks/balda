// Package chatarchiveapp owns immutable conversational archive admission policy.
package chatarchiveapp

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
)

// InputStore atomically records input and fills unknown historical location fields.
type InputStore interface {
	RecordInput(ctx context.Context, message chatarchivecmd.Message, location chatarchivecmd.Location) (chatarchivecmd.Message, error)
}

// Service records original input independently of prompt and delivery policy.
type Service struct{ store InputStore }

// New constructs archive admission with the selected shared database capability.
func New(store InputStore) *Service { return &Service{store: store} }

// RecordInput preserves first observation and never rebinds a retry to a new lifetime.
func (s *Service) RecordInput(ctx context.Context, archiveID string, input chatarchivecmd.Input) (chatarchivecmd.Message, error) {
	if s == nil || s.store == nil {
		return chatarchivecmd.Message{}, chatarchivecmd.ErrUnavailable
	}
	if strings.TrimSpace(archiveID) == "" || strings.TrimSpace(input.Source) == "" || input.ReceivedAt.IsZero() || strings.TrimSpace(input.Speaker.Principal) == "" {
		return chatarchivecmd.Message{}, chatarchivecmd.ErrInvalid
	}
	attachments := make([]chatarchivecmd.Attachment, 0, len(input.Attachments))
	for _, a := range input.Attachments {
		attachments = append(attachments, chatarchivecmd.AttachmentLabel(a.Name, a.Kind))
	}
	m := chatarchivecmd.Message{SessionID: archiveID, Source: "input:" + input.Source, Role: "user", Text: input.Text, Speaker: input.Speaker, Attachments: attachments, ObservedAt: input.ReceivedAt.UTC()}
	return s.store.RecordInput(ctx, m, input.Location)
}
