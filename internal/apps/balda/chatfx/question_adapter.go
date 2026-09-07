package chatfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
)

// QuestionAdapter adapts questions.Service to chatapp.QuestionResolver.
type QuestionAdapter struct {
	questions *questions.Service
}

// NewQuestionAdapter constructs a QuestionAdapter.
func NewQuestionAdapter(questions *questions.Service) *QuestionAdapter {
	return &QuestionAdapter{questions: questions}
}

// ResolveQuestionReply resolves a question reply using questions.Service.
func (a *QuestionAdapter) ResolveQuestionReply(ctx context.Context, reply questioncmd.InboundReply) (chatapp.QuestionResolution, error) {
	if a == nil || a.questions == nil {
		return chatapp.QuestionResolution{}, nil
	}
	res, err := a.questions.ResolveReplyDetailed(ctx, reply)
	if err != nil {
		return chatapp.QuestionResolution{Matched: res.Matched}, err
	}
	return chatapp.QuestionResolution{
		Matched:      res.Matched,
		Settled:      res.Settled,
		Continuation: res.Continuation,
	}, nil
}
