package chatarchivefx

import (
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
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
		reply := questioncmd.InboundReply{Provider: "telegram", ConversationKey: "1:0", ReplyToMessageID: id.QuestionID, MessageID: "reply:" + id.QuestionID, Text: "  literal answer  ", User: questioncmd.UserRef{UserID: "speaker"}, ReceivedAt: at, OriginalInput: &chatarchivecmd.Input{Text: "  literal answer  ", Speaker: chatarchivecmd.Speaker{Principal: "speaker", Name: "Anna"}, ReceivedAt: at, Location: archive.Location}}
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
