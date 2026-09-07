package webhookapp

import (
	"context"
	"errors"
	"testing"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type fakeTargetResolver struct {
	resolveFn func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error)
}

func (f *fakeTargetResolver) ResolveTarget(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
	if f.resolveFn != nil {
		return f.resolveFn(ctx, target)
	}
	return envelopetarget.Resolved{
		Locator: deliverycmd.Locator{
			ChannelType: "telegram",
			AddressKey:  "-100123:456",
			SessionID:   "session-123",
		},
		Principal: "user-456",
	}, nil
}

type fakeSessionPublisher struct {
	lastPayload *turncmd.SessionTurnPayload
	receipt     *actortransport.DispatchReceipt
	err         error
}

func (f *fakeSessionPublisher) PublishSessionTurn(_ context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
	f.lastPayload = &payload
	if f.err != nil {
		return nil, f.err
	}
	if f.receipt != nil {
		return f.receipt, nil
	}
	return &actortransport.DispatchReceipt{
		MsgID:    "msg-session-1",
		Stream:   "balda.cmd.session",
		Sequence: 42,
	}, nil
}

type fakeJobPublisher struct {
	lastPayload   *turncmd.SessionTurnPayload
	lastRouteName string
	lastRequestID string
	receipt       *actortransport.DispatchReceipt
	jobID         string
	err           error
}

func (f *fakeJobPublisher) PublishWebhookJob(_ context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
	f.lastPayload = &payload
	f.lastRouteName = routeName
	f.lastRequestID = requestID
	if f.err != nil {
		return nil, "", f.err
	}
	if f.receipt != nil {
		return f.receipt, f.jobID, nil
	}
	return &actortransport.DispatchReceipt{
		MsgID:     "msg-job-1",
		Stream:    "balda.cmd.job",
		Sequence:  101,
		Duplicate: false,
	}, "job-101", nil
}

func TestService_Accept_JobMode_Success(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	sessionPub := &fakeSessionPublisher{}
	jobPub := &fakeJobPublisher{}
	svc := NewService(resolver, sessionPub, jobPub)

	req := Request{
		RequestID: "req-1",
		RouteName: "alert_route",
		Prompt:    "Disk is full",
		Target: envelopetarget.Target{
			Target: "locator",
			Key:    "telegram:-100123:456",
		},
		Mode:      ModeJob,
		DedupeKey: "webhook:alert_route:req-1",
	}

	res, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	if res.RequestID != "req-1" {
		t.Errorf("res.RequestID = %q, want req-1", res.RequestID)
	}
	if res.MessageID != "msg-job-1" {
		t.Errorf("res.MessageID = %q, want msg-job-1", res.MessageID)
	}
	if res.JobID != "job-101" {
		t.Errorf("res.JobID = %q, want job-101", res.JobID)
	}
	if res.Stream != "balda.cmd.job" || res.Sequence != 101 {
		t.Errorf("res stream/seq = %s/%d, want balda.cmd.job/101", res.Stream, res.Sequence)
	}
	if jobPub.lastPayload == nil {
		t.Fatal("jobPub did not receive payload")
	}
	if jobPub.lastPayload.Text != "Disk is full" {
		t.Errorf("payload text = %q, want 'Disk is full'", jobPub.lastPayload.Text)
	}
	if jobPub.lastPayload.Deliver {
		t.Errorf("payload deliver = true, want false when ReportTo is nil")
	}
	if jobPub.lastPayload.Source != "webhook" {
		t.Errorf("payload source = %q, want webhook", jobPub.lastPayload.Source)
	}
	if jobPub.lastPayload.DedupeKey != "webhook:alert_route:req-1" {
		t.Errorf("payload dedupe key = %q, want webhook:alert_route:req-1", jobPub.lastPayload.DedupeKey)
	}
	if jobPub.lastRouteName != "alert_route" || jobPub.lastRequestID != "req-1" {
		t.Errorf("jobPub route/reqID = %s/%s", jobPub.lastRouteName, jobPub.lastRequestID)
	}
	if sessionPub.lastPayload != nil {
		t.Errorf("sessionPub should not have been called")
	}
}

func TestService_Accept_SessionMode_Success(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	sessionPub := &fakeSessionPublisher{}
	jobPub := &fakeJobPublisher{}
	svc := NewService(resolver, sessionPub, jobPub)

	reportToTarget := envelopetarget.Target{
		Target: "locator",
		Key:    "telegram:report:1",
	}

	req := Request{
		RequestID: "req-sess",
		RouteName: "chat_route",
		Prompt:    "hello from hook",
		Target: envelopetarget.Target{
			Target: "locator",
			Key:    "telegram:chat:1",
		},
		ReportTo:  &reportToTarget,
		Mode:      ModeSession,
		DedupeKey: "webhook:chat_route:req-sess",
	}

	res, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	if res.RequestID != "req-sess" {
		t.Errorf("res.RequestID = %q, want req-sess", res.RequestID)
	}
	if res.MessageID != "msg-session-1" {
		t.Errorf("res.MessageID = %q, want msg-session-1", res.MessageID)
	}
	if sessionPub.lastPayload == nil {
		t.Fatal("sessionPub did not receive payload")
	}
	if !sessionPub.lastPayload.Deliver {
		t.Errorf("payload deliver = false, want true when ReportTo is set")
	}
	if sessionPub.lastPayload.ReportTo == nil {
		t.Errorf("payload ReportTo is nil")
	}
	if jobPub.lastPayload != nil {
		t.Errorf("jobPub should not have been called")
	}
}

func TestService_Accept_TargetNotFound(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("unknown alias: nonexistent")
	resolver := &fakeTargetResolver{
		resolveFn: func(_ context.Context, _ envelopetarget.Target) (envelopetarget.Resolved, error) {
			return envelopetarget.Resolved{}, expectedErr
		},
	}
	svc := NewService(resolver, &fakeSessionPublisher{}, &fakeJobPublisher{})

	req := Request{
		RequestID: "req-err",
		RouteName: "route",
		Prompt:    "hello",
		Target: envelopetarget.Target{
			Target: "alias",
			Key:    "nonexistent",
		},
	}

	_, err := svc.Accept(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsTargetNotFound(err) {
		t.Errorf("IsTargetNotFound(err) = false, want true; err = %v", err)
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("errors.Is(err, expectedErr) = false; err = %v", err)
	}
}

func TestService_Accept_ReportToTargetNotFound(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("report alias not found")
	resolver := &fakeTargetResolver{
		resolveFn: func(_ context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
			if target.Key == "bad-report" {
				return envelopetarget.Resolved{}, expectedErr
			}
			return envelopetarget.Resolved{Locator: deliverycmd.Locator{SessionID: "ok"}}, nil
		},
	}
	svc := NewService(resolver, &fakeSessionPublisher{}, &fakeJobPublisher{})

	reportTo := envelopetarget.Target{Target: "alias", Key: "bad-report"}
	req := Request{
		RequestID: "req-rep-err",
		RouteName: "route",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "alias", Key: "ok"},
		ReportTo:  &reportTo,
	}

	_, err := svc.Accept(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsTargetNotFound(err) {
		t.Errorf("IsTargetNotFound(err) = false, want true; err = %v", err)
	}
}

func TestService_Accept_QueueFull(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	jobPub := &fakeJobPublisher{err: actorcmd.ErrCommandQueueFull}
	svc := NewService(resolver, &fakeSessionPublisher{}, jobPub)

	req := Request{
		RequestID: "req-qf",
		RouteName: "route",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
		Mode:      ModeJob,
	}

	_, err := svc.Accept(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsQueueFull(err) {
		t.Errorf("IsQueueFull(err) = false, want true; err = %v", err)
	}
}

func TestService_Accept_DispatchFailed(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	sessionPub := &fakeSessionPublisher{err: errors.New("nats broker unavailable")}
	svc := NewService(resolver, sessionPub, &fakeJobPublisher{})

	req := Request{
		RequestID: "req-df",
		RouteName: "route",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
		Mode:      ModeSession,
	}

	_, err := svc.Accept(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsDispatchFailed(err) {
		t.Errorf("IsDispatchFailed(err) = false, want true; err = %v", err)
	}
	if IsQueueFull(err) {
		t.Errorf("IsQueueFull(err) = true, want false")
	}
}

func TestService_Accept_InvalidRequest(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	svc := NewService(resolver, &fakeSessionPublisher{}, &fakeJobPublisher{})

	// Missing request ID
	_, err := svc.Accept(context.Background(), Request{
		Prompt: "hello",
		Target: envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
	})
	if err == nil || !IsInvalidRequest(err) {
		t.Errorf("expected InvalidRequest for empty RequestID, got %v", err)
	}

	// Missing prompt
	_, err = svc.Accept(context.Background(), Request{
		RequestID: "req-1",
		Prompt:    "   ",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
	})
	if err == nil || !IsInvalidRequest(err) {
		t.Errorf("expected InvalidRequest for whitespace prompt, got %v", err)
	}
}

func TestService_Accept_MissingPublishers(t *testing.T) {
	t.Parallel()

	resolver := &fakeTargetResolver{}
	// Nil job publisher in job mode
	svcNoJob := NewService(resolver, &fakeSessionPublisher{}, nil)
	_, err := svcNoJob.Accept(context.Background(), Request{
		RequestID: "req-1",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
		Mode:      ModeJob,
	})
	if err == nil || !IsDispatchFailed(err) {
		t.Errorf("expected DispatchFailedError for nil jobPublisher, got %v", err)
	}

	// Nil session publisher in session mode
	svcNoSess := NewService(resolver, nil, &fakeJobPublisher{})
	_, err = svcNoSess.Accept(context.Background(), Request{
		RequestID: "req-1",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
		Mode:      ModeSession,
	})
	if err == nil || !IsDispatchFailed(err) {
		t.Errorf("expected DispatchFailedError for nil sessionPublisher, got %v", err)
	}

	// Nil target resolver
	svcNoResolver := NewService(nil, &fakeSessionPublisher{}, &fakeJobPublisher{})
	_, err = svcNoResolver.Accept(context.Background(), Request{
		RequestID: "req-1",
		Prompt:    "hello",
		Target:    envelopetarget.Target{Target: "locator", Key: "telegram:1:2"},
	})
	if err == nil || !IsDispatchFailed(err) {
		t.Errorf("expected DispatchFailedError for nil targetResolver, got %v", err)
	}
}

func TestAdapters(t *testing.T) {
	t.Parallel()

	calledTarget := false
	tf := TargetResolverFunc(func(_ context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
		calledTarget = true
		return envelopetarget.Resolved{Locator: deliverycmd.Locator{SessionID: target.Key}}, nil
	})
	res, err := tf.ResolveTarget(context.Background(), envelopetarget.Target{Key: "test-sess"})
	if err != nil || !calledTarget || res.Locator.SessionID != "test-sess" {
		t.Errorf("TargetResolverFunc failed: %v, %v, %v", err, calledTarget, res)
	}

	calledSession := false
	sf := SessionPublisherFunc(func(_ context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
		calledSession = true
		return &actortransport.DispatchReceipt{MsgID: payload.Text}, nil
	})
	sReceipt, sErr := sf.PublishSessionTurn(context.Background(), turncmd.SessionTurnPayload{Text: "hi"})
	if sErr != nil || !calledSession || sReceipt.MsgID != "hi" {
		t.Errorf("SessionPublisherFunc failed: %v, %v, %v", sErr, calledSession, sReceipt)
	}

	calledJob := false
	jf := JobPublisherFunc(func(_ context.Context, _ turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
		calledJob = true
		return &actortransport.DispatchReceipt{MsgID: routeName}, requestID, nil
	})
	jReceipt, jID, jErr := jf.PublishWebhookJob(context.Background(), turncmd.SessionTurnPayload{}, "my-route", "req-99")
	if jErr != nil || !calledJob || jReceipt.MsgID != "my-route" || jID != "req-99" {
		t.Errorf("JobPublisherFunc failed: %v, %v, %v, %v", jErr, calledJob, jReceipt, jID)
	}
}
