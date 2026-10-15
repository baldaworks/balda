package mattermostfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
)

// NewPromptRegistryContribution registers Mattermost Markdown as this
// transport's model-authored presentation format.
func NewPromptRegistryContribution() deliveryfx.PromptRegistryContribution {
	rule, example := mattermost.FormattingPromptRuleAndExample()
	return deliveryfx.PromptRegistryContribution{
		Formats: []deliveryfmt.Format{
			{Name: deliveryfmt.NameMattermostMarkdown, Instructions: rule, Example: example},
		},
		Formatters: []deliveryfmt.FormatterRegistration{
			{Name: deliveryfmt.NameMattermostMarkdown, Formatter: markdownFormatter{}},
		},
		Routes: []deliveryfmt.Route{
			{
				Transport:      deliveryfmt.TransportMattermost,
				DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
				RegisteredName: deliveryfmt.NameMattermostMarkdown,
			},
		},
	}
}

type markdownFormatter struct{}

func (markdownFormatter) Name() deliveryfmt.Name {
	return deliveryfmt.NameMattermostMarkdown
}

func (markdownFormatter) Format(text string) (deliveryfmt.Message, error) {
	return deliveryfmt.Message{
		Name:          deliveryfmt.NameMattermostMarkdown,
		Text:          text,
		PlainFallback: mattermost.MarkdownPlainText(text),
	}, nil
}

type mattermostLocatorRenderer struct{}

type mattermostQuestionRenderer struct{}

type mattermostPermissionRenderer struct{}

func (mattermostQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderQuestion(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewQuestionStructuredRegistrar registers Mattermost question presentation.
func NewQuestionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(
		deliveryfmt.TransportMattermost,
		questionfmt.RequestDescriptor,
		mattermostQuestionRenderer{},
	)
}

func (mattermostPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderPermission(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewPermissionStructuredRegistrar registers Mattermost permission prompts.
func NewPermissionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(
		deliveryfmt.TransportMattermost,
		permissionfmt.RequestDescriptor,
		mattermostPermissionRenderer{},
	)
}

func (mattermostLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewLocatorStructuredRegistrar registers Mattermost locator presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(
		deliveryfmt.TransportMattermost,
		locatorfmt.ResponseDescriptor,
		mattermostLocatorRenderer{},
	)
}

type mattermostGoalProgressRenderer struct{}

func (mattermostGoalProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalProgress]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

type mattermostGoalOutcomeRenderer struct{}

func (mattermostGoalOutcomeRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalOutcome]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalOutcome(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewGoalProgressStructuredRegistrar registers Mattermost goal progress presentation.
func NewGoalProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(
		deliveryfmt.TransportMattermost,
		goalkeepercmd.ProgressDescriptor,
		mattermostGoalProgressRenderer{},
	)
}

// NewGoalOutcomeStructuredRegistrar registers Mattermost goal outcome presentation.
func NewGoalOutcomeStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(
		deliveryfmt.TransportMattermost,
		goalkeepercmd.OutcomeDescriptor,
		mattermostGoalOutcomeRenderer{},
	)
}
