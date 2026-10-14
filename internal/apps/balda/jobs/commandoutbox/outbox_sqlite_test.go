package commandoutbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type testTransport struct {
	fail bool
	ids  []string
}

func (d *testTransport) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if d.fail {
		return nil, errors.New("NATS unavailable")
	}
	d.ids = append(d.ids, env.ID)
	return &actortransport.DispatchReceipt{Stream: "commands", Sequence: 1, MsgID: env.ID}, nil
}

func TestCommandSurvivesSQLiteCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "balda.db")
	provider, err := state.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	transport := &testTransport{fail: true}
	d, err := commandoutbox.NewDispatcher(provider.AppKV(), transport, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	env := actorlayer.Envelope{ID: "original-command", Namespace: "balda", Kind: "turn", From: actorlayer.SystemAddress("ingress"), To: actorlayer.SystemAddress("session"), DedupeKey: "original-post", Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: []byte(`{"text":"retained task"}`)}}
	if receipt, err := d.Dispatch(ctx, env); err != nil || receipt == nil {
		t.Fatalf("durable admission failed: %v", err)
	}
	keys, err := provider.AppKV().List(ctx, "command-outbox/pending/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("pending record missing: %v %v", keys, err)
	}
	raw, _, _ := provider.AppKV().Get(ctx, keys[0])
	var record map[string]any
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	delete(record, "next_attempt")
	data, _ := json.Marshal(record)
	if err := provider.AppKV().Set(ctx, keys[0], string(data)); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err = state.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	transport.fail = false
	restarted, err := commandoutbox.NewDispatcher(provider.AppKV(), transport, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transport.ids) != 1 || transport.ids[0] != env.ID {
		t.Fatalf("incorrect recovery: %v", transport.ids)
	}
	if _, err := restarted.Dispatch(ctx, env); err != nil {
		t.Fatal(err)
	}
	if len(transport.ids) != 1 {
		t.Fatalf("duplicate executed: %v", transport.ids)
	}

	keys, err = provider.AppKV().List(ctx, "command-outbox/receipts/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("receipt missing: %v %v", keys, err)
	}
	raw, _, err = provider.AppKV().Get(ctx, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	record["retain_until"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	data, _ = json.Marshal(record)
	if err := provider.AppKV().Set(ctx, keys[0], string(data)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := provider.AppKV().Get(ctx, keys[0]); err != nil || found {
		t.Fatalf("expired receipt still stored: found=%v err=%v", found, err)
	}
}
