package chatfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

// ChatServiceParams specifies dependencies for the conversational ingress service.
type ChatServiceParams struct {
	fx.In

	SessionManager *baldasession.Manager
	Dispatcher     actortransport.Dispatcher
	Questions      *questions.Service `optional:"true"`
	Logger         zerolog.Logger
}

// NewChatService constructs the canonical chatapp.Handler for the container.
func NewChatService(params ChatServiceParams) (chatapp.Handler, error) {
	var qResolver chatapp.QuestionResolver
	if params.Questions != nil {
		qResolver = NewQuestionAdapter(params.Questions)
	}
	return chatapp.NewService(chatapp.ServiceParams{
		Sessions:   NewSessionAdapter(params.SessionManager),
		Dispatcher: NewDispatcherAdapter(params.Dispatcher),
		Questions:  qResolver,
		Logger:     params.Logger,
	})
}

// Module wires conversational ingress into the balda container.
var Module = fx.Module("balda_chatfx",
	fx.Provide(
		NewChatService,
	),
)
