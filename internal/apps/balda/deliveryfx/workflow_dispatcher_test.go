package deliveryfx

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
)

func TestApplyFormattedMessageTargetsOnlyFormattedContent(t *testing.T) {
	t.Parallel()

	const (
		rawText       = "raw"
		formattedText = "formatted"
	)
	message := &deliveryfmt.Message{Name: deliveryfmt.NameTelegramRichMarkdown, Text: formattedText}
	for _, test := range []struct {
		name    string
		payload deliverycmd.Payload
		assert  func(*testing.T, deliverycmd.Payload)
	}{
		{
			name:    "agent reply",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeAgentReply, Text: rawText},
			assert: func(t *testing.T, payload deliverycmd.Payload) {
				if payload.Text != formattedText {
					t.Fatalf("text = %q, want formatted", payload.Text)
				}
			},
		},
		{
			name:    "progress",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeProgress, Progress: &deliverycmd.Progress{Text: rawText}},
			assert: func(t *testing.T, payload deliverycmd.Payload) {
				if payload.Progress == nil || payload.Progress.Text != formattedText {
					t.Fatalf("progress = %+v, want formatted", payload.Progress)
				}
			},
		},
		{
			name:    "caption",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeDocument, Media: &deliverycmd.Media{Caption: rawText}},
			assert: func(t *testing.T, payload deliverycmd.Payload) {
				if payload.Media == nil || payload.Media.Caption != formattedText {
					t.Fatalf("media = %+v, want formatted caption", payload.Media)
				}
			},
		},
		{
			name:    "plain operational message",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModePlain, Text: rawText},
			assert: func(t *testing.T, payload deliverycmd.Payload) {
				if payload.Text != rawText {
					t.Fatalf("plain text = %q, want raw operational text", payload.Text)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalProgress := test.payload.Progress
			originalMedia := test.payload.Media
			got := applyFormattedMessage(test.payload, message)
			test.assert(t, got)
			if originalProgress != nil && originalProgress.Text != rawText {
				t.Fatalf("original progress mutated: %+v", originalProgress)
			}
			if originalMedia != nil && originalMedia.Caption != rawText {
				t.Fatalf("original media mutated: %+v", originalMedia)
			}
		})
	}
}
