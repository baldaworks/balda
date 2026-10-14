package questionoutbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type recoverySource struct {
	pending        map[string]bool
	cleanup        map[string]bool
	bad            string
	cleanupFailure bool
}

func (s *recoverySource) PendingQuestionAnswerIDs(_ context.Context, after string, limit int) ([]string, error) {
	var ids []string
	for id := range s.pending {
		if id > after && (s.pending[id] || s.cleanup[id]) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids[:min(len(ids), limit)], nil
}
func (s *recoverySource) AnsweredContinuation(_ context.Context, id string) (actorlayer.Envelope, bool, error) {
	if id == s.bad {
		return actorlayer.Envelope{}, false, errors.New("invalid stored answer")
	}
	return actorlayer.Envelope{ID: id}, s.pending[id], nil
}
func (s *recoverySource) ConfirmAnswerAdmission(_ context.Context, id string) error {
	s.pending[id] = false
	if s.cleanupFailure {
		return errors.New("control admission failed")
	}
	s.cleanup[id] = false
	return nil
}

type recoveryAdmitter struct {
	ids       []string
	fail      bool
	noReceipt bool
}

func (d *recoveryAdmitter) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if d.fail {
		return nil, errors.New("local admission unavailable")
	}
	if d.noReceipt {
		return nil, nil
	}
	d.ids = append(d.ids, env.ID)
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

func TestRecoveryRetriesAdmissionAndKeepsCleanupIndependent(t *testing.T) {
	for _, failure := range []string{"error", "no_receipt"} {
		t.Run(failure, func(t *testing.T) {
			s := &recoverySource{pending: map[string]bool{"answer": true}, cleanup: map[string]bool{"answer": true}}
			d := &recoveryAdmitter{fail: failure == "error", noReceipt: failure == "no_receipt"}
			r, err := New(s, d, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Flush(t.Context()); err == nil || !s.pending["answer"] || !s.cleanup["answer"] {
				t.Fatal("failed admission released durable recovery state")
			}
			d.fail, d.noReceipt = false, false
			s.cleanupFailure = true
			if err := r.Flush(t.Context()); err == nil || s.pending["answer"] || !s.cleanup["answer"] {
				t.Fatal("cleanup failure lost its independent marker")
			}
			s.cleanupFailure = false
			if err := r.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(d.ids) != 1 || s.pending["answer"] || s.cleanup["answer"] {
				t.Fatalf("cleanup replayed admitted answer or retained handoff: %v", d.ids)
			}
		})
	}
}

func TestRecoveryCursorMakesProgressPastInvalidAnswers(t *testing.T) {
	s := &recoverySource{pending: map[string]bool{}, cleanup: map[string]bool{}, bad: "answer-00"}
	for i := range 40 {
		id := fmt.Sprintf("answer-%02d", i)
		s.pending[id] = true
		s.cleanup[id] = true
	}
	d := &recoveryAdmitter{}
	r, err := New(s, d, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(t.Context()); err == nil {
		t.Fatal("invalid answer was not reported")
	}
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.ids) != 39 || !s.pending[s.bad] {
		t.Fatalf("valid answers starved or bad answer forgotten: %v", d.ids)
	}
}
