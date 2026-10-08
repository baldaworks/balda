package webhookapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type admissionMemory struct {
	mu      sync.Mutex
	records map[string]webhookcmd.Admission
}

func (m *admissionMemory) Get(_ context.Context, route, dedupe string) (webhookcmd.Admission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.records[route+"/"+dedupe]
	return a, ok, nil
}

func (m *admissionMemory) Create(_ context.Context, candidate webhookcmd.Admission) (webhookcmd.Admission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = make(map[string]webhookcmd.Admission)
	}
	key := candidate.RouteName + "/" + candidate.DedupeKey
	if existing, ok := m.records[key]; ok {
		return existing, false, nil
	}
	m.records[key] = candidate
	return candidate, true, nil
}

func (m *admissionMemory) RecordReceipt(_ context.Context, route, dedupe string, receipt webhookcmd.Receipt) (webhookcmd.Admission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := route + "/" + dedupe
	a := m.records[key]
	if a.MessageID == "" {
		a.MessageID, a.Stream, a.Sequence = receipt.MessageID, receipt.Stream, receipt.Sequence
		m.records[key] = a
	}
	return a, nil
}

type testResolver struct {
	mu      sync.Mutex
	locator deliverycmd.Locator
	err     error
	calls   int
}

func (r *testResolver) ResolveTarget(_ context.Context, _ envelopetarget.Target) (envelopetarget.Resolved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return envelopetarget.Resolved{}, r.err
	}
	return envelopetarget.Resolved{Locator: r.locator}, nil
}

type testPublisher struct {
	mu       sync.Mutex
	payloads []turncmd.SessionTurnPayload
	failNext error
}

func (p *testPublisher) PublishWebhookJob(_ context.Context, payload turncmd.SessionTurnPayload, route, requestID string) (*actortransport.DispatchReceipt, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.payloads = append(p.payloads, payload)
	if p.failNext != nil {
		err := p.failNext
		p.failNext = nil
		return nil, "", err
	}
	_, jobID, err := turncmd.WebhookJobEnvelope(payload, route, requestID)
	if err != nil {
		return nil, "", err
	}
	return &actortransport.DispatchReceipt{MsgID: "msg-1", Stream: "balda.cmd.job", Sequence: 1}, jobID, nil
}

func webhookRequest(dedupe string) Request {
	return Request{RequestID: "req-1", RouteName: "events", Prompt: "event payload", DedupeKey: dedupe}
}

func TestAcceptCreatesPrivateJobWithoutDestination(t *testing.T) {
	store, pub := &admissionMemory{}, &testPublisher{}
	svc := NewService(nil, store, pub)
	result, err := svc.Accept(t.Context(), webhookRequest("webhook:events:1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID == "" || result.MessageID == "" || result.Duplicate {
		t.Fatalf("unexpected result: %+v", result)
	}
	payload := pub.payloads[0]
	if payload.Locator.ChannelType != "webhook" || !turncmd.IsPrivateRun(turncmd.SourceWebhook, payload.Locator.SessionID) ||
		payload.Locator.AddressKey != payload.Locator.SessionID || payload.ReportTo != nil || payload.Deliver {
		t.Fatalf("unexpected private payload: %+v", payload)
	}
	second, err := svc.Accept(t.Context(), webhookRequest("webhook:events:1"))
	if err != nil || !second.Duplicate || second.JobID != result.JobID || second.MessageID != result.MessageID || len(pub.payloads) != 1 {
		t.Fatalf("duplicate = %+v, err = %v, publications = %d", second, err, len(pub.payloads))
	}
}

func TestAcceptFreezesReportLocatorAcrossRetargetAndRetry(t *testing.T) {
	original := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat:1", AddressJSON: `{}`, SessionID: "tg-1"}
	resolver := &testResolver{locator: original}
	store, pub := &admissionMemory{}, &testPublisher{failNext: errors.New("temporary dispatch failure")}
	svc := NewService(resolver, store, pub)
	req := webhookRequest("webhook:events:stable")
	req.ReportTo = &envelopetarget.Target{Target: "managed_alias", Key: "main_chat"}
	if _, err := svc.Accept(t.Context(), req); !IsDispatchFailed(err) {
		t.Fatalf("first acceptance error = %v", err)
	}
	first, found, err := store.Get(t.Context(), req.RouteName, req.DedupeKey)
	if err != nil || !found || !reflect.DeepEqual(first.ReportTo, &original) || first.MessageID != "" {
		t.Fatalf("stored admission = %+v, found = %t, err = %v", first, found, err)
	}
	resolver.mu.Lock()
	resolver.locator = deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat:2", AddressJSON: `{}`, SessionID: "tg-2"}
	resolver.mu.Unlock()
	got, err := svc.Accept(t.Context(), req)
	if err != nil || !got.Duplicate || got.JobID != first.JobID || got.MessageID == "" {
		t.Fatalf("retry = %+v, err = %v", got, err)
	}
	if !reflect.DeepEqual(pub.payloads[0], pub.payloads[1]) || !reflect.DeepEqual(pub.payloads[1].ReportTo, &original) ||
		pub.payloads[1].Locator.SessionID != first.SessionID || resolver.calls != 1 {
		t.Fatalf("retarget changed frozen publication: %+v", pub.payloads)
	}
}

func TestAcceptMissingAliasDoesNotPublish(t *testing.T) {
	resolver := &testResolver{err: envelopetarget.ErrDestinationUnavailable}
	store, pub := &admissionMemory{}, &testPublisher{}
	req := webhookRequest("webhook:events:missing")
	req.ReportTo = &envelopetarget.Target{Target: "managed_alias", Key: "missing"}
	_, err := NewService(resolver, store, pub).Accept(t.Context(), req)
	if !IsTargetNotFound(err) || len(pub.payloads) != 0 {
		t.Fatalf("result error = %v, publications = %d", err, len(pub.payloads))
	}
	if _, found, _ := store.Get(t.Context(), req.RouteName, req.DedupeKey); found {
		t.Fatal("missing alias created admission")
	}
}

func TestAcceptConcurrentAdmissionSurvivesAliasDeletion(t *testing.T) {
	store, pub := &admissionMemory{}, &testPublisher{}
	req := webhookRequest("webhook:events:accepted")
	req.ReportTo = &envelopetarget.Target{Target: "managed_alias", Key: "main_chat"}
	resolved := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat:1", AddressJSON: `{}`, SessionID: "tg-1"}
	resolver := TargetResolverFunc(func(ctx context.Context, _ envelopetarget.Target) (envelopetarget.Resolved, error) {
		candidate := webhookcmd.Admission{
			RouteName: req.RouteName, DedupeKey: req.DedupeKey, RequestID: req.RequestID,
			Prompt: req.Prompt, SessionID: "wh-existing", ReportTo: &resolved, CreatedAt: time.Now().UTC(),
		}
		_, candidate.JobID, _ = turncmd.WebhookJobEnvelope(payloadFromAdmission(candidate), req.RouteName, req.RequestID)
		_, _, err := store.Create(ctx, candidate)
		if err != nil {
			t.Fatal(err)
		}
		return envelopetarget.Resolved{}, envelopetarget.ErrDestinationUnavailable
	})
	result, err := NewService(resolver, store, pub).Accept(t.Context(), req)
	if err != nil || !result.Duplicate || len(pub.payloads) != 1 || !reflect.DeepEqual(pub.payloads[0].ReportTo, &resolved) {
		t.Fatalf("accepted duplicate = %+v, err = %v, publications = %+v", result, err, pub.payloads)
	}
}

func TestAcceptDistinctAndConcurrentRequestsHaveStablePrivateSessions(t *testing.T) {
	store, pub := &admissionMemory{}, &testPublisher{}
	svc := NewService(nil, store, pub)
	const n = 16
	results := make([]Result, n)
	errs := make([]error, n)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[i], errs[i] = svc.Accept(t.Context(), webhookRequest("webhook:events:concurrent"))
		}()
	}
	group.Wait()
	for i := range results {
		if errs[i] != nil || results[i].JobID != results[0].JobID || results[i].MessageID != results[0].MessageID {
			t.Fatalf("request %d = %+v, err = %v", i, results[i], errs[i])
		}
	}
	first, _, _ := store.Get(t.Context(), "events", "webhook:events:concurrent")
	second, err := svc.Accept(t.Context(), webhookRequest("webhook:events:other"))
	if err != nil || second.JobID == first.JobID {
		t.Fatalf("distinct request = %+v, err = %v", second, err)
	}
	other, _, _ := store.Get(t.Context(), "events", "webhook:events:other")
	if first.SessionID == other.SessionID || !strings.HasPrefix(other.SessionID, "wh-") {
		t.Fatalf("session IDs = %s / %s", first.SessionID, other.SessionID)
	}
	for _, payload := range pub.payloads {
		if payload.DedupeKey == first.DedupeKey && payload.Locator.SessionID != first.SessionID {
			t.Fatalf("concurrent duplicate changed session: %+v", payload)
		}
	}
}

func TestAcceptQueueBackpressurePreservesAdmission(t *testing.T) {
	store, pub := &admissionMemory{}, &testPublisher{failNext: actorcmd.ErrCommandQueueFull}
	svc := NewService(nil, store, pub)
	req := webhookRequest("webhook:events:busy")
	_, err := svc.Accept(t.Context(), req)
	if !IsQueueFull(err) {
		t.Fatalf("Accept() error = %v", err)
	}
	if _, found, _ := store.Get(t.Context(), req.RouteName, req.DedupeKey); !found {
		t.Fatal("backpressure lost admission")
	}
}
