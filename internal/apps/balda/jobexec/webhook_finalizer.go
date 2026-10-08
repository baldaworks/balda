package jobexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// WebhookRunStore exposes only the durable cleanup state needed by this use case.
type WebhookRunStore interface {
	ListUnclosedTerminalWebhookJobs(ctx context.Context, after time.Time, afterID string, before time.Time, limit int) ([]state.JobRecord, error)
	MarkWebhookRunClosed(ctx context.Context, jobID string, closedAt time.Time) error
	RecordPrivateOutput(ctx context.Context, jobID, output string, failed bool) error
}

type WebhookAdmissionReader interface {
	GetByJobID(ctx context.Context, jobID string) (webhookcmd.Admission, bool, error)
}

type WebhookFinalDeliveryReader interface {
	FinalDelivery(ctx context.Context, jobID string) (state.DeliveryRecord, bool, error)
}

type WebhookRunSessionCloser interface {
	CloseRunSession(ctx context.Context, sessionID, userID string) error
}

// WebhookRunFinalizer removes private runtime state after the job and report settle.
type WebhookRunFinalizer struct {
	runs        WebhookRunStore
	admissions  WebhookAdmissionReader
	deliveries  WebhookFinalDeliveryReader
	sessions    WebhookRunSessionCloser
	dispatcher  actortransport.Dispatcher
	logger      zerolog.Logger
	now         func() time.Time
	mu          sync.Mutex
	lastAt      time.Time
	lastID      string
	sweepBefore time.Time
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewWebhookRunFinalizer(runs WebhookRunStore, admissions WebhookAdmissionReader,
	deliveries WebhookFinalDeliveryReader, sessions WebhookRunSessionCloser,
	dispatcher actortransport.Dispatcher, logger zerolog.Logger) *WebhookRunFinalizer {
	return &WebhookRunFinalizer{
		runs: runs, admissions: admissions, deliveries: deliveries, sessions: sessions,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.webhook_run_finalizer").Logger(), now: time.Now,
	}
}

func (f *WebhookRunFinalizer) Start(context.Context) error {
	if f == nil || f.runs == nil || f.admissions == nil || f.deliveries == nil || f.sessions == nil || f.dispatcher == nil {
		return fmt.Errorf("webhook run finalizer is unavailable")
	}
	if f.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := f.Settle(ctx, 100); err != nil && ctx.Err() == nil {
				f.logger.Warn().Err(err).Msg("failed to finalize webhook runs")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (f *WebhookRunFinalizer) Stop(ctx context.Context) error {
	if f == nil || f.cancel == nil {
		return nil
	}
	f.cancel()
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Settle scans terminal webhook jobs and retries idempotent private-session cleanup.
func (f *WebhookRunFinalizer) Settle(ctx context.Context, limit int) error {
	if f == nil || f.runs == nil || f.admissions == nil || f.deliveries == nil || f.sessions == nil || f.dispatcher == nil {
		return fmt.Errorf("webhook run finalizer is unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sweepBefore.IsZero() {
		f.sweepBefore = f.now().UTC()
	}
	jobs, err := f.runs.ListUnclosedTerminalWebhookJobs(ctx, f.lastAt, f.lastID, f.sweepBefore, limit)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		f.lastAt, f.lastID = time.Time{}, ""
		f.sweepBefore = f.now().UTC()
		jobs, err = f.runs.ListUnclosedTerminalWebhookJobs(ctx, f.lastAt, f.lastID, f.sweepBefore, limit)
		if err != nil {
			return err
		}
	}
	if len(jobs) > 0 {
		last := jobs[len(jobs)-1]
		f.lastAt, f.lastID = last.CreatedAt, last.ID
	}
	var failures error
	for _, job := range jobs {
		if err := f.settleJob(ctx, job); err != nil {
			failures = errors.Join(failures, fmt.Errorf("settle webhook job %q: %w", job.ID, err))
		}
	}
	return failures
}

func (f *WebhookRunFinalizer) settleJob(ctx context.Context, job state.JobRecord) error {
	admission, found, err := f.admissions.GetByJobID(ctx, job.ID)
	if err != nil {
		return err
	}
	if !found || admission.SessionID != job.SessionID || strings.TrimSpace(job.CreatedBy) == "" {
		return fmt.Errorf("webhook admission and private session do not match")
	}
	if job.Result == "" {
		const failureOutput = "The webhook run could not complete. Ask the operator to check Balda."
		if err := f.runs.RecordPrivateOutput(ctx, job.ID, failureOutput, true); err != nil {
			return fmt.Errorf("record terminal webhook output: %w", err)
		}
		job.Result = failureOutput
	}
	if admission.ReportTo != nil {
		delivery, found, err := f.deliveries.FinalDelivery(ctx, job.ID)
		if err != nil {
			return err
		}
		if !found {
			report, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement(job.ID,
				actorlayer.ActorAddress{Target: actorcmd.ActorTypeSession, Key: job.SessionID},
				*admission.ReportTo, deliveryfmt.DeliveryFormatNone, deliverycmd.SettlementOutbox,
				job.Result, "final")
			if err != nil {
				return fmt.Errorf("build terminal webhook delivery: %w", err)
			}
			if _, err := f.dispatcher.Dispatch(ctx, report); err != nil {
				return fmt.Errorf("dispatch terminal webhook delivery: %w", err)
			}
			return nil
		}
		if delivery.Status != state.DeliveryStatusSent &&
			(delivery.Status != state.DeliveryStatusFailed || delivery.Error != "permanent") {
			return nil
		}
	}
	if err := f.sessions.CloseRunSession(ctx, job.SessionID, job.CreatedBy); err != nil {
		return err
	}
	return f.runs.MarkWebhookRunClosed(ctx, job.ID, f.now().UTC())
}
