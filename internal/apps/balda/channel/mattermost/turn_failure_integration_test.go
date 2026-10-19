package mattermost_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/mattermostfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

func TestTurnFailureRegistryPreservesLiteralPostAndContentRejection(t *testing.T) {
	const original = "Provider error:\nМодель **busy** [label](https://example.com) `code`\n```\n````\n&amp; <busy>"
	const want = "`````\n" + original + "\n`````"
	for _, tt := range []struct {
		name   string
		status int
		reject bool
	}{
		{name: "success", status: http.StatusCreated},
		{name: "HTTP content rejection", status: http.StatusBadRequest, reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var formats deliveryfmt.PromptRegistry
			var structured deliveryfmt.StructuredMessageRegistry
			app := fx.New(fx.NopLogger, deliveryfx.Module,
				fx.Provide(
					fx.Annotate(mattermostfx.NewPromptRegistryContribution, fx.ResultTags(`group:"balda_delivery_prompt_contribution"`)),
					fx.Annotate(mattermostfx.NewTurnFailureStructuredRegistrar, fx.ResultTags(`group:"balda_delivery_structured_registrar"`)),
				), fx.Populate(&formats, &structured))
			if err := app.Err(); err != nil {
				t.Fatal(err)
			}
			env := deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]{Descriptor: deliveryfmt.TurnFailureDescriptor, Body: deliveryfmt.TurnFailure{Text: original}}
			rendered, err := structured.RenderStructured(t.Context(), "mattermost", env.Type(), env)
			if err != nil {
				t.Fatal(err)
			}
			_, _, formatter, err := formats.Resolve("mattermost", rendered.DeliveryFormat)
			if err != nil {
				t.Fatal(err)
			}
			message, err := formatter.Format(rendered.Text)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var posted []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Message string `json:"message"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode post: %v", err)
				}
				text := body.Message
				mu.Lock()
				posted = append(posted, text)
				attempt := len(posted)
				mu.Unlock()
				if tt.reject && attempt == 1 {
					w.WriteHeader(tt.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "api.post.create_post.bad_request", "message": "synthetic content rejection"})
					return
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "post-created-1"})
			}))
			t.Cleanup(server.Close)
			adapter := mattermost.NewAdapter(mattermost.NewClient(server.URL, "synthetic-token", "bot-1"), zerolog.Nop())
			result, err := adapter.Deliver(t.Context(), mattermost.NewChannelLocator("team-1", "channel-1", ""), deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Message: &message})
			if tt.reject {
				var apiErr *mattermost.APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.status || apiErr.ID != "api.post.create_post.bad_request" || apiErr.Message != "synthetic content rejection" || result.ProviderMessageID != "" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err != nil || result.ProviderMessageID != "post-created-1" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posted) != 1 || posted[0] != want {
				t.Fatalf("posts=%q, want one exact fenced provider final %q", posted, want)
			}
		})
	}
}
