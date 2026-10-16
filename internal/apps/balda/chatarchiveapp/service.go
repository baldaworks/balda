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

// FinalStore atomically commits a selected final with its execution constituents.
type FinalStore interface {
	CommitFinal(ctx context.Context, message chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error)
	FindFinal(ctx context.Context, epoch, member string) (chatarchivecmd.Message, error)
}

// CommitFinal selects exact immutable visible text before publication.
func (s *Service) CommitFinal(ctx context.Context, m chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error) {
	if m.Role != "agent" || strings.TrimSpace(m.Text) == "" || m.SessionID == "" || m.Source == "" || len(m.Replay) == 0 || len(members) == 0 {
		return m, chatarchivecmd.ErrInvalid
	}
	store, ok := s.store.(FinalStore)
	if !ok {
		return m, chatarchivecmd.ErrUnavailable
	}
	return store.CommitFinal(ctx, m, members)
}

// FindFinal reads committed output without invoking execution or delivery.
func (s *Service) FindFinal(ctx context.Context, epoch, member string) (chatarchivecmd.Message, error) {
	store, ok := s.store.(FinalStore)
	if !ok {
		return chatarchivecmd.Message{}, chatarchivecmd.ErrUnavailable
	}
	return store.FindFinal(ctx, epoch, member)
}
