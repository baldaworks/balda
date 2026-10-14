package commandoutbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type sessionOrderTransport struct {
	failID    string
	attempted []string
	published []string
}

func (b *sessionOrderTransport) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	b.attempted = append(b.attempted, env.ID)
	if env.ID == b.failID {
		return nil, errors.New("publication unavailable")
	}
	b.published = append(b.published, env.ID)
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

func TestSessionOrderAcrossIngressTargetsSurvivesRestart(t *testing.T) {
	for _, first := range []string{"command", "chat", "question", "address"} {
		t.Run(first, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "state.db")
			p, err := state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if p != nil {
					_ = p.Close()
				}
			})
			envs := sessionIngressEnvelopes(t, "session-1")
			other := sessionIngressEnvelopes(t, "session-2")[2]
			switch first {
			case "chat":
				envs = []actorlayer.Envelope{envs[1], envs[2], envs[0]}
			case "question":
				envs = []actorlayer.Envelope{envs[2], envs[0], envs[1]}
			case "address":
				envs = []actorlayer.Envelope{addressEnvelope("first", "maintenance"), addressEnvelope("second", "maintenance")}
				other = addressEnvelope("other", "delivery")
			}
			bus := &sessionOrderTransport{failID: envs[0].ID}
			d, err := commandoutbox.NewDispatcher(p.AppKV(), bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			for _, env := range append(slices.Clone(envs), other) {
				if receipt, err := d.Dispatch(ctx, env); err != nil || receipt == nil {
					t.Fatalf("admission = %+v, %v", receipt, err)
				}
			}
			if err := d.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(bus.attempted, []string{envs[0].ID, other.ID}) || !slices.Equal(bus.published, []string{other.ID}) {
				t.Fatalf("failed session bypassed or independent scope blocked: attempted=%v published=%v", bus.attempted, bus.published)
			}
			setPublicationRetry(t, p.AppKV(), envs[0].ID, time.Now().Add(time.Hour))
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			p, err = state.NewSQLiteProvider(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			d, err = commandoutbox.NewDispatcher(p.AppKV(), bus, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			bus.failID = ""
			if err := d.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if len(bus.attempted) != 2 {
				t.Fatalf("restart bypassed pending backoff: %v", bus.attempted)
			}
			setPublicationRetry(t, p.AppKV(), envs[0].ID, time.Now().Add(-time.Hour))
			if err := d.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			want := []string{other.ID}
			for _, env := range envs {
				want = append(want, env.ID)
			}
			if !slices.Equal(bus.published, want) {
				t.Fatalf("recovered publication order = %v, want %v", bus.published, want)
			}
		})
	}
}

func sessionIngressEnvelopes(t *testing.T, sessionID string) []actorlayer.Envelope {
	t.Helper()
	locator := deliverycmd.Locator{SessionID: sessionID, ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1}`}
	command, err := commandcmd.NewEnvelope(commandcmd.Payload{
		Version: commandcmd.SchemaVersion, Name: "locator", SnapshotID: "snapshot-1", Transport: "telegram", Principal: "tg-1", Locator: locator,
	}, commandcmd.EnvelopeOptions{ID: "command-" + sessionID, From: actorlayer.SystemAddress("ingress")})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := turncmd.SessionTurnEnvelope(turncmd.SessionTurnPayload{Text: "next message", Locator: locator})
	if err != nil {
		t.Fatal(err)
	}
	question, err := questioncmd.AnsweredEnvelope(questioncmd.ResumeTarget{To: "session:" + sessionID}, questioncmd.InteractionContext{SessionID: sessionID, Locator: locator}, questioncmd.Answer{Text: "yes"}, "question-"+sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return []actorlayer.Envelope{command, chat, question}
}

func addressEnvelope(id, target string) actorlayer.Envelope {
	return actorlayer.Envelope{ID: id, Namespace: "balda", Kind: "turn", From: actorlayer.SystemAddress("ingress"), To: actorlayer.ActorAddress{Target: target, Key: "shared"}, Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: []byte(`{}`)}}
}

func setPublicationRetry(t *testing.T, store commandoutbox.OutboxStore, id string, when time.Time) {
	t.Helper()
	keys, err := store.List(t.Context(), "command-outbox/pending/")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		raw, _, err := store.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			t.Fatal(err)
		}
		var wire string
		if err := json.Unmarshal(record["envelope"], &wire); err != nil {
			t.Fatal(err)
		}
		env, err := actorlayer.DecodeEnvelope(wire)
		if err != nil {
			t.Fatal(err)
		}
		if env.ID != id {
			continue
		}
		record["next_attempt"], err = json.Marshal(when.UTC())
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set(t.Context(), key, string(data)); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("pending command %s not found", id)
}
