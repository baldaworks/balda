package deliveryfx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/mattermostfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/slackagentfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram/telegramfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip/zulipfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestSessionActivationRenderersResolveAcrossTransports(t *testing.T) {
	registry := sessionRegistry(t)
	fixtures := []struct {
		name     string
		body     sessionfmt.Notice
		icon     string
		headline string
	}{
		{name: "created", body: noticeFixture(t, sessioncmd.ActivationCreated), icon: "🚀", headline: "Session Created"},
		{name: "restore fallback", body: noticeFixture(t, sessioncmd.ActivationReplaced), icon: "♻️", headline: "Session Replaced"},
	}
	transports := []struct {
		name           string
		value          string
		format         deliveryfmt.DeliveryFormat
		escapedLabel   string
		headlinePrefix string
	}{
		{name: "telegram", value: deliveryfmt.TransportTelegram, format: deliveryfmt.DeliveryFormatRichMarkdown, escapedLabel: `name_*[]<>&`, headlinePrefix: "**"},
		{name: "zulip", value: deliveryfmt.TransportZulip, format: deliveryfmt.DeliveryFormatMarkdown, escapedLabel: `name_*[]<>&`, headlinePrefix: "**"},
		{name: "slack", value: deliveryfmt.TransportSlackAgent, format: deliveryfmt.DeliveryFormatMrkdwn, escapedLabel: `name_*[]&lt;&gt;&amp;`, headlinePrefix: "*"},
		{name: "mattermost", value: deliveryfmt.TransportMattermost, format: deliveryfmt.DeliveryFormatMarkdown, escapedLabel: `name_*[]<>&`, headlinePrefix: "**"},
	}
	for _, transport := range transports {
		for _, fixture := range fixtures {
			t.Run(transport.name+"/"+fixture.name, func(t *testing.T) {
				envelope, err := fixture.body.Envelope()
				if err != nil {
					t.Fatal(err)
				}
				presentation, err := deliveryfmt.RenderStructured(t.Context(), registry, transport.value, envelope)
				if err != nil {
					t.Fatalf("RenderStructured() error = %v", err)
				}
				wantPrefix := fixture.icon + " " + transport.headlinePrefix + fixture.headline + transport.headlinePrefix
				if presentation.DeliveryFormat != transport.format || !strings.HasPrefix(presentation.Text, wantPrefix) || !strings.Contains(presentation.Text, transport.escapedLabel) {
					t.Fatalf("presentation = %+v", presentation)
				}
				for _, want := range []string{"Name:", "ID:", "Model:", "Reasoning:", "Type:", "MCP:"} {
					if !strings.Contains(presentation.Text, want) {
						t.Fatalf("presentation missing %q: %q", want, presentation.Text)
					}
				}
				for _, absent := range []string{"Provider:", "Provider type:", "\n"} {
					if strings.Contains(presentation.Text, absent) {
						t.Fatalf("presentation contains %q: %q", absent, presentation.Text)
					}
				}
				for _, absent := range []string{"provider_*", "activation-private"} {
					if strings.Contains(presentation.Text, absent) {
						t.Fatalf("presentation contains private data %q: %q", absent, presentation.Text)
					}
				}
			})
		}

		t.Run(transport.name+"/unknown runtime omitted", func(t *testing.T) {
			body := noticeFixture(t, sessioncmd.ActivationCreated)
			body.Label = ""
			body.Runtime = sessioncmd.RuntimeSummary{}
			envelope, err := body.Envelope()
			if err != nil {
				t.Fatal(err)
			}
			presentation, err := deliveryfmt.RenderStructured(t.Context(), registry, transport.value, envelope)
			if err != nil {
				t.Fatal(err)
			}
			for _, absent := range []string{"Name:", "Provider:", "Provider type:", "Model:", "Reasoning:", "Type:", "MCP:"} {
				if strings.Contains(presentation.Text, absent) {
					t.Fatalf("unknown field %q rendered: %q", absent, presentation.Text)
				}
			}
		})

		t.Run(transport.name+"/warning", func(t *testing.T) {
			warning, err := sessionfmt.NewWarning("session-1", sessioncmd.WarningWorkspaceSyncSkipped)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := warning.Envelope()
			if err != nil {
				t.Fatal(err)
			}
			presentation, err := deliveryfmt.RenderStructured(t.Context(), registry, transport.value, envelope)
			if err != nil || presentation.DeliveryFormat != transport.format || !strings.Contains(presentation.Text, "Workspace sync skipped") {
				t.Fatalf("warning presentation = %+v, error = %v", presentation, err)
			}
		})
	}
}

func TestSessionActivationCompositionFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		options []fx.Option
	}{
		{
			name: "missing required warning renderer",
			options: []fx.Option{
				provideRegistrar(func(registry *deliveryfmt.StructuredRegistry) error {
					return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportTelegram, sessionfmt.NoticeDescriptor, testNoticeRenderer{})
				}),
				provideRequirements(telegramfx.NewSessionStructuredRequirements),
			},
		},
		{
			name: "duplicate renderer",
			options: []fx.Option{
				provideRegistrar(telegramfx.NewSessionStructuredRegistrar()),
				provideRegistrar(telegramfx.NewSessionStructuredRegistrar()),
			},
		},
		{
			name: "invalid requirement",
			options: []fx.Option{
				provideRequirements(func() []deliveryfx.StructuredRegistryRequirement {
					return []deliveryfx.StructuredRegistryRequirement{{Transport: " Telegram ", MessageType: sessionfmt.NoticeDescriptor.Type}}
				}),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []fx.Option{fx.NopLogger, deliveryfx.Module}
			options = append(options, test.options...)
			if err := fx.New(options...).Err(); err == nil {
				t.Fatal("fx.New() succeeded")
			}
		})
	}
}

func TestSessionActivationRendererRejectsRestoredNotice(t *testing.T) {
	registry := sessionRegistry(t)
	restored := noticeFixture(t, sessioncmd.ActivationCreated)
	restored.Kind = sessioncmd.ActivationRestored
	_, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[sessionfmt.Notice]{
		Descriptor: sessionfmt.NoticeDescriptor,
		Body:       restored,
	})
	if !errors.Is(err, sessionfmt.ErrInvalid) {
		t.Fatalf("RenderStructured() error = %v, want ErrInvalid", err)
	}
}

func sessionRegistry(t *testing.T) *deliveryfmt.StructuredRegistry {
	t.Helper()
	var registry *deliveryfmt.StructuredRegistry
	app := fxtest.New(t,
		fx.NopLogger,
		deliveryfx.Module,
		provideRegistrar(telegramfx.NewSessionStructuredRegistrar()),
		provideRegistrar(zulipfx.NewSessionStructuredRegistrar()),
		provideRegistrar(slackagentfx.NewSessionStructuredRegistrar()),
		provideRegistrar(mattermostfx.NewSessionStructuredRegistrar()),
		provideRequirements(telegramfx.NewSessionStructuredRequirements),
		provideRequirements(zulipfx.NewSessionStructuredRequirements),
		provideRequirements(slackagentfx.NewSessionStructuredRequirements),
		provideRequirements(mattermostfx.NewSessionStructuredRequirements),
		fx.Populate(&registry),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	return registry
}

func provideRegistrar(registrar deliveryfx.StructuredRegistryRegistrar) fx.Option {
	return fx.Provide(fx.Annotate(
		func() deliveryfx.StructuredRegistryRegistrar { return registrar },
		fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
	))
}

func provideRequirements(constructor any) fx.Option {
	return fx.Provide(fx.Annotate(
		constructor,
		fx.ResultTags(`group:"balda_delivery_structured_requirement,flatten"`),
	))
}

func noticeFixture(t *testing.T, kind sessioncmd.ActivationKind) sessionfmt.Notice {
	t.Helper()
	activation := sessioncmd.Activation{
		Kind: kind, SessionID: "session-1", Label: "name_*[]<>&",
		Runtime: sessioncmd.RuntimeSummary{
			PublicRuntimeMetadata: sessioncmd.PublicRuntimeMetadata{
				ProviderID: "provider_*<>&", ProviderType: "codex_acp", ReasoningEffort: "high", MCPServers: []string{"balda", "tool_*"},
			},
			EffectiveModel: "model_*<>&",
		},
	}
	notice, err := sessionfmt.NewNotice(activation)
	if err != nil {
		t.Fatal(err)
	}
	return notice
}

type testNoticeRenderer struct{}

func (testNoticeRenderer) RenderStructured(context.Context, deliveryfmt.StructuredEnvelope[sessionfmt.Notice]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{}, nil
}
