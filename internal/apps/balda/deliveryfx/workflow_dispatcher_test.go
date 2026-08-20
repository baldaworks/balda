package deliveryfx

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestChannelOperationCarriesTypedFormattedContent(t *testing.T) {
	t.Parallel()

	const (
		rawText       = "raw"
		formattedText = "formatted"
	)
	message := &deliveryfmt.Message{Name: deliveryfmt.NameTelegramRichMarkdown, Text: formattedText}
	for _, test := range []struct {
		name    string
		payload deliverycmd.Payload
		assert  func(*testing.T, deliverycmd.Operation)
	}{
		{
			name:    "agent reply",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeAgentReply, Text: rawText},
			assert: func(t *testing.T, operation deliverycmd.Operation) {
				if operation.Text != formattedText || operation.Message != message {
					t.Fatalf("operation = %+v, want typed formatted message", operation)
				}
			},
		},
		{
			name:    "progress",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeProgress, Progress: &deliverycmd.Progress{Text: rawText}},
			assert: func(t *testing.T, operation deliverycmd.Operation) {
				if operation.Progress.Text != formattedText || operation.Message != message {
					t.Fatalf("operation = %+v, want typed formatted progress", operation)
				}
			},
		},
		{
			name:    "caption",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModeDocument, Media: &deliverycmd.Media{Caption: rawText}},
			assert: func(t *testing.T, operation deliverycmd.Operation) {
				if operation.Media == nil || operation.Media.Caption != formattedText || operation.Message != message {
					t.Fatalf("operation = %+v, want typed formatted caption", operation)
				}
			},
		},
		{
			name:    "plain operational message",
			payload: deliverycmd.Payload{Mode: deliverycmd.ModePlain, Text: rawText},
			assert: func(t *testing.T, operation deliverycmd.Operation) {
				if operation.Text != rawText {
					t.Fatalf("plain text = %q, want raw operational text", operation.Text)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalProgress := test.payload.Progress
			originalMedia := test.payload.Media
			got, err := channelOperation(test.payload, message)
			if err != nil {
				t.Fatalf("channelOperation() error = %v", err)
			}
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
