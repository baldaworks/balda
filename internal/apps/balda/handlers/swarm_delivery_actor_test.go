package handlers

import (
	"context"
	"encoding/json"
	"testing"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
)

func TestTaskDeliveryActorDeduplicatesSentDelivery(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	actor := &taskDeliveryActor{
		channel: baldatelegram.NewAdapter(baldatelegram.AdapterParams{
			Messenger: msg,
			TGClient:  tgClient,
			Logger:    zerolog.Nop(),
		}),
		tasks:  tasks,
		logger: zerolog.Nop(),
	}

	locator := baldatelegram.NewLocator(9001, 99)
	payload := taskDeliveryPayload{TaskID: "task-1", Locator: locator, Text: "Goal started"}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := swarm.Envelope{
		ID:          "delivery-command-1",
		Namespace:   swarm.NamespaceAgentResult,
		Kind:        taskPayloadKindDelivery,
		From:        swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: "task-1"},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeDelivery, Key: "9001:99"},
		SessionID:   locator.SessionID,
		TaskID:      "task-1",
		DedupeKey:   "task-1:delivery:started",
		PayloadJSON: string(data),
	}

	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() first error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() duplicate error = %v", err)
	}
	if got := len(tgClient.messages); got != 1 {
		t.Fatalf("sent telegram messages = %d, want 1", got)
	}
}

func TestTaskDeliveryActorDefersDuplicatePendingDelivery(t *testing.T) {
	ctx := context.Background()
	provider, bus, coordinator, tasks, allocator := newTaskActorSwarmServices(t, ctx)
	_ = provider
	_ = bus
	_ = coordinator
	_ = allocator
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	msg.SetAgentReplyFormattingMode("none")
	actor := &taskDeliveryActor{
		channel: baldatelegram.NewAdapter(baldatelegram.AdapterParams{
			Messenger: msg,
			TGClient:  tgClient,
			Logger:    zerolog.Nop(),
		}),
		tasks:  tasks,
		logger: zerolog.Nop(),
	}

	locator := baldatelegram.NewLocator(9001, 99)
	payload := taskDeliveryPayload{TaskID: "task-1", Locator: locator, Text: "Goal started"}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := swarm.Envelope{
		ID:          "delivery-command-pending",
		Namespace:   swarm.NamespaceAgentResult,
		Kind:        taskPayloadKindDelivery,
		From:        swarm.ActorAddress{Target: swarm.ActorTypeTask, Key: "task-1"},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeDelivery, Key: "9001:99"},
		SessionID:   locator.SessionID,
		TaskID:      "task-1",
		DedupeKey:   "task-1:delivery:pending",
		PayloadJSON: string(data),
	}
	if _, _, err := tasks.ReserveDelivery(ctx, deliveryRecordForTest(env, payload, baldastate.SwarmDeliveryStatusPending)); err != nil {
		t.Fatalf("ReserveDelivery() error = %v", err)
	}
	if err := actor.Handle(ctx, env); swarm.ClassifyError(err) != swarm.ErrorKindTransient {
		t.Fatalf("Handle() error kind = %s, want transient: %v", swarm.ClassifyError(err), err)
	}
	if got := len(tgClient.messages); got != 0 {
		t.Fatalf("sent telegram messages = %d, want 0 while duplicate is pending", got)
	}
}

func deliveryRecordForTest(env swarm.Envelope, payload taskDeliveryPayload, status string) baldastate.SwarmDeliveryRecord {
	return baldastate.SwarmDeliveryRecord{
		ID:          "delivery-record-" + env.ID,
		DeliveryKey: deliveryKeyForEnvelope(env),
		TaskID:      payload.TaskID,
		SessionID:   payload.Locator.SessionID,
		Channel:     "telegram",
		AddressKey:  payload.Locator.AddressKey,
		Kind:        env.Kind,
		PayloadJSON: env.PayloadJSON,
		PayloadHash: hashDeliveryPayload(env.PayloadJSON),
		Status:      status,
	}
}
