package zulip_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip/zulipfx"
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
		{name: "success", status: http.StatusOK},
		{name: "HTTP content rejection", status: http.StatusBadRequest, reject: true},
		{name: "API content rejection", status: http.StatusOK, reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var formats deliveryfmt.PromptRegistry
			var structured deliveryfmt.StructuredMessageRegistry
			app := fx.New(fx.NopLogger, deliveryfx.Module,
				fx.Provide(
					fx.Annotate(zulipfx.NewPromptRegistryContribution, fx.ResultTags(`group:"balda_delivery_prompt_contribution"`)),
					fx.Annotate(zulipfx.NewTurnFailureStructuredRegistrar, fx.ResultTags(`group:"balda_delivery_structured_registrar"`)),
				), fx.Populate(&formats, &structured))
			if err := app.Err(); err != nil {
				t.Fatal(err)
			}
			env := deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]{Descriptor: deliveryfmt.TurnFailureDescriptor, Body: deliveryfmt.TurnFailure{Text: original}}
			rendered, err := structured.RenderStructured(t.Context(), "zulip", env.Type(), env)
			if err != nil {
				t.Fatal(err)
			}
			_, _, formatter, err := formats.Resolve("zulip", rendered.DeliveryFormat)
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
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse form: %v", err)
				}
				text := r.Form.Get("content")
				mu.Lock()
				posted = append(posted, text)
				attempt := len(posted)
				mu.Unlock()
				if tt.reject && attempt == 1 {
					w.WriteHeader(tt.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"result": "error", "code": "BAD_REQUEST", "msg": "synthetic content rejection"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "id": 456})
			}))
			t.Cleanup(server.Close)
			adapter := zulip.NewAdapter(zulip.NewClient(server.URL, "bot@example.com", "synthetic-key"), zerolog.Nop())
			result, err := adapter.Deliver(t.Context(), zulip.NewStreamLocator(42, "synthetic-topic"), deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Message: &message})
			if tt.reject {
				var apiErr *zulip.APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.status || apiErr.Code != "BAD_REQUEST" || apiErr.Message != "synthetic content rejection" || result.ProviderMessageID != "" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err != nil || result.ProviderMessageID != "456" {
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
