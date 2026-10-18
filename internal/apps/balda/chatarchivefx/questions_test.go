package chatarchivefx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestPrivateQuestionDoesNotInheritPublicArchive(t *testing.T) {
	a := &questionArchive{}
	for _, test := range []struct {
		name, target string
		private      bool
	}{{"permission", actorcmd.ActorTypePermission, false}, {"private conversation", actorcmd.ActorTypeSession, true}, {"goal", actorcmd.ActorTypeGoalkeeper, false}} {
		t.Run(test.name, func(t *testing.T) {
			interaction, err := a.BindQuestion(t.Context(), questioncmd.InteractionContext{SessionID: "public", ArchiveID: "epoch", Private: test.private}, questioncmd.ResumeTarget{To: test.target + ":public"})
			if err != nil || interaction.ArchiveID != "" {
				t.Fatalf("private answer can enter archive: %+v,%v", interaction, err)
			}
			if err := a.RecordAnswer(t.Context(), interaction, "q", questioncmd.InboundReply{Text: "allow once"}); err != nil {
				t.Fatalf("private answer recording blocked settlement: %v", err)
			}
		})
	}
}

func TestQuestionAnswersCaptureOnlyPublicFreeText(t *testing.T) {
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Now().UTC()
	archive := chatarchivecmd.Session{ID: "33c9805d-c079-4f04-a52b-77f0ce015d47", StableID: "public", RuntimeID: "runtime", Persistence: "sqlite", BootID: "boot", StartedAt: at, ObservedAt: at, Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}}
	if _, err := p.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: "create", Kind: "fresh", Session: archive, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChatArchive().Activate(t.Context(), "create", nil); err != nil {
		t.Fatal(err)
	}
	adapter := &questionArchive{inputs: chatarchiveapp.New(p.ChatArchive()), sessions: p.Sessions()}
	svc := questions.New(p.Questions(), nil, zerolog.Nop())
	svc.SetArchive(adapter)
	for _, private := range []bool{false, true} {
		id, err := svc.Ask(t.Context(), questioncmd.InteractionContext{ArchiveID: archive.ID, SessionID: "public", Private: private, Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`, SessionID: "public"}}, questioncmd.ResumeTarget{To: "session:public"}, questioncmd.Request{Prompt: "Question", AllowFreeText: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.BindDelivery(t.Context(), id.QuestionID, questioncmd.DeliveryRef{Provider: "telegram", ConversationKey: "1:0", ProviderMessageID: id.QuestionID}); err != nil {
			t.Fatal(err)
		}
		reply := questioncmd.InboundReply{Provider: "telegram", ConversationKey: "1:0", ReplyToMessageID: id.QuestionID, MessageID: "reply:" + id.QuestionID, Text: "  literal answer  ", User: questioncmd.UserRef{UserID: "speaker"}, ReceivedAt: at, OriginalInput: &chatarchivecmd.Input{Source: "telegram:1:0:message:" + id.QuestionID, Text: "  literal answer  ", Speaker: chatarchivecmd.Speaker{Principal: "speaker", Name: "Anna"}, ReceivedAt: at, Location: archive.Location}}
		for range 2 {
			result, err := svc.ResolveReplyDetailed(t.Context(), reply)
			if err != nil || !result.Settled {
				t.Fatalf("answer settlement = %+v,%v", result, err)
			}
		}
	}
	messages, err := p.ChatArchive().Messages(t.Context(), archive.ID, 0, 100)
	if err != nil || len(messages) != 1 || messages[0].Text != "  literal answer  " || messages[0].Speaker.Name != "Anna" {
		t.Fatalf("public transcript = %+v,%v", messages, err)
	}
}

// competingAnswerStore settles another provider reply immediately after the pending lookup.
type competingAnswerStore struct {
	state.QuestionStore
	winner questioncmd.Answer
}

func (s *competingAnswerStore) GetQuestionByReplyRef(ctx context.Context, provider, conversation, reply string) (state.QuestionRecord, bool, error) {
	pending, found, err := s.QuestionStore.GetQuestionByReplyRef(ctx, provider, conversation, reply)
	if err == nil && found && pending.Status == questioncmd.StatusPending {
		_, _, err = s.MarkQuestionAnswered(ctx, pending.QuestionID, s.winner)
	}
	return pending, found, err
}

func TestQuestionCASLoserSharesOrdinaryInputIdentity(t *testing.T) {
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Now().UTC()
	epoch := chatarchivecmd.Session{ID: "33c9805d-c079-4f04-a52b-77f0ce015d47", StableID: "public", RuntimeID: "runtime", Persistence: "sqlite", BootID: "boot", StartedAt: at, ObservedAt: at, Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}}
	if _, err := p.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: "create", Kind: "fresh", Session: epoch, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChatArchive().Activate(t.Context(), "create", nil); err != nil {
		t.Fatal(err)
	}
	inputs := chatarchiveapp.New(p.ChatArchive())
	store := &competingAnswerStore{QuestionStore: p.Questions(), winner: questioncmd.Answer{Text: "winner", ProviderMsgID: "100", AnsweredBy: questioncmd.UserRef{UserID: "speaker"}, AnsweredAt: at}}
	svc := questions.New(store, nil, zerolog.Nop())
	svc.SetArchive(&questionArchive{inputs: inputs, sessions: p.Sessions()})
	q, err := svc.Ask(t.Context(), questioncmd.InteractionContext{ArchiveID: epoch.ID, SessionID: "public", Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`, SessionID: "public"}}, questioncmd.ResumeTarget{To: "session:public"}, questioncmd.Request{Prompt: "Question", AllowFreeText: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindDelivery(t.Context(), q.QuestionID, questioncmd.DeliveryRef{Provider: "telegram", ConversationKey: "1:0", ProviderMessageID: "question-message"}); err != nil {
		t.Fatal(err)
	}
	original := chatarchivecmd.Input{Source: "telegram:1:0:message:101", Text: "losing answer", Speaker: chatarchivecmd.Speaker{Principal: "speaker"}, ReceivedAt: at, Location: epoch.Location}
	result, err := svc.ResolveReplyDetailed(t.Context(), questioncmd.InboundReply{Provider: "telegram", ConversationKey: "1:0", ReplyToMessageID: "question-message", MessageID: "101", Text: original.Text, User: questioncmd.UserRef{UserID: "speaker"}, ReceivedAt: at, OriginalInput: &original})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched {
		t.Fatalf("expected ordinary-input fallthrough: %+v", result)
	}
	if _, err := inputs.RecordInput(t.Context(), epoch.ID, original); err != nil {
		t.Fatal(err)
	}
	rows, err := p.ChatArchive().Messages(t.Context(), epoch.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("same provider reply archived %d times: %+v", len(rows), rows)
	}
	if rows[0].Source != "input:"+original.Source || rows[0].Text != original.Text {
		t.Fatalf("input changed: %+v", rows[0])
	}
	// Replaying the losing reply still takes ordinary admission and retains one row.
	result, err = svc.ResolveReplyDetailed(t.Context(), questioncmd.InboundReply{Provider: "telegram", ConversationKey: "1:0", ReplyToMessageID: "question-message", MessageID: "101", User: questioncmd.UserRef{UserID: "speaker"}})
	if err != nil || result.Matched {
		t.Fatalf("losing reply replay = %+v, %v", result, err)
	}
	if _, err := p.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: "reset", Kind: "destroy", Session: epoch, OldID: epoch.ID, Reason: "reset", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := p.ChatArchive().Complete(t.Context(), "reset", "destroyed"); err != nil {
		t.Fatal(err)
	}
	replacement := epoch
	replacement.ID = "43c9805d-c079-4f04-a52b-77f0ce015d47"
	replacement.PredecessorID = epoch.ID
	if _, err := p.ChatArchive().Begin(t.Context(), chatarchivecmd.Intent{Token: "replacement", Kind: "fresh", Session: replacement, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChatArchive().Activate(t.Context(), "replacement", nil); err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.ReceivedAt = at.Add(time.Minute)
	recorded, err := inputs.RecordInput(t.Context(), replacement.ID, retry)
	if err != nil || recorded.SessionID != epoch.ID || !recorded.ObservedAt.Equal(at) {
		t.Fatalf("retry rebound original input: %+v, %v", recorded, err)
	}
	retry.Text = "conflicting answer"
	if _, err := inputs.RecordInput(t.Context(), replacement.ID, retry); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("conflicting replay = %v", err)
	}
	fresh := original
	fresh.Source = "telegram:1:0:message:102"
	fresh.Text = "new ordinary reply"
	recorded, err = inputs.RecordInput(t.Context(), replacement.ID, fresh)
	if err != nil || recorded.SessionID != replacement.ID {
		t.Fatalf("new ordinary reply = %+v, %v", recorded, err)
	}
	rows, err = p.ChatArchive().Messages(t.Context(), epoch.ID, 0, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("original transcript = %+v, %v", rows, err)
	}
	rows, err = p.ChatArchive().Messages(t.Context(), replacement.ID, 0, 100)
	if err != nil || len(rows) != 1 || rows[0].Text != fresh.Text {
		t.Fatalf("replacement transcript = %+v, %v", rows, err)
	}
}
