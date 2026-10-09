package webhookapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/google/uuid"
)

// AdmissionStore is the policy owner's narrow port for one frozen request.
type AdmissionStore interface {
	Get(ctx context.Context, routeName, dedupeKey string) (webhookcmd.Admission, bool, error)
	Create(ctx context.Context, candidate webhookcmd.Admission) (webhookcmd.Admission, bool, error)
	RecordReceipt(ctx context.Context, routeName, dedupeKey string, receipt webhookcmd.Receipt) (webhookcmd.Admission, error)
}

// Service admits a webhook once and publishes its frozen private job.
type Service struct {
	targetResolver TargetResolver
	admissions     AdmissionStore
	jobPublisher   JobPublisher
}

func NewService(targetResolver TargetResolver, admissions AdmissionStore, jobPub JobPublisher) *Service {
	return &Service{targetResolver: targetResolver, admissions: admissions, jobPublisher: jobPub}
}

// Accept returns the original admission for every duplicate, including retries
// after a publication failure. SQL stores the snapshot; the actor transport
// still owns durable dispatch and command deduplication.
func (s *Service) Accept(ctx context.Context, req Request) (Result, error) {
	reqID, routeName, dedupeKey := strings.TrimSpace(req.RequestID), strings.TrimSpace(req.RouteName), strings.TrimSpace(req.DedupeKey)
	prompt := strings.TrimSpace(req.Prompt)
	if reqID == "" || len(reqID) > webhookcmd.MaxRequestIDBytes ||
		routeName == "" || len(routeName) > webhookcmd.MaxRouteNameBytes ||
		dedupeKey == "" || len(dedupeKey) > webhookcmd.MaxDedupeKeyBytes ||
		prompt == "" || len(prompt) > webhookcmd.MaxPromptBytes || len(req.RawBody) > webhookcmd.MaxBodyBytes {
		return Result{}, &InvalidRequestError{Field: "webhook", Message: "required input is empty or too large"}
	}
	if s.admissions == nil || s.jobPublisher == nil {
		return Result{}, &DispatchFailedError{Cause: errors.New("webhook admission or publication is unavailable")}
	}
	selected, found, err := s.admissions.Get(ctx, routeName, dedupeKey)
	if err != nil {
		return Result{}, &DispatchFailedError{Cause: err}
	}
	created := false
	if !found {
		candidate, admissionErr := s.newAdmission(ctx, req, reqID, routeName, dedupeKey, prompt)
		if admissionErr != nil {
			if IsTargetNotFound(admissionErr) {
				selected, found, err = s.admissions.Get(ctx, routeName, dedupeKey)
				if err != nil {
					return Result{}, &DispatchFailedError{Cause: err}
				}
				if !found {
					return Result{}, admissionErr
				}
			} else {
				return Result{}, admissionErr
			}
		}
		if !found {
			selected, created, err = s.admissions.Create(ctx, candidate)
			if err != nil {
				return Result{}, &DispatchFailedError{Cause: err}
			}
		}
	}
	if selected.MessageID != "" {
		return resultFromAdmission(selected, true), nil
	}
	payload := payloadFromAdmission(selected)
	receipt, jobID, err := s.jobPublisher.PublishWebhookJob(ctx, payload, selected.RouteName, selected.RequestID)
	if err != nil {
		if actorcmd.IsCommandQueueFull(err) {
			return Result{}, &QueueFullError{Cause: err}
		}
		return Result{}, &DispatchFailedError{Cause: err}
	}
	if receipt == nil || receipt.MsgID == "" || len(receipt.MsgID) > webhookcmd.MaxReceiptMessageBytes ||
		receipt.Stream == "" || len(receipt.Stream) > webhookcmd.MaxReceiptStreamBytes ||
		receipt.Sequence > math.MaxInt64 || jobID != selected.JobID {
		return Result{}, &DispatchFailedError{Cause: fmt.Errorf("webhook publisher returned an inconsistent receipt")}
	}
	selected, err = s.admissions.RecordReceipt(ctx, selected.RouteName, selected.DedupeKey,
		webhookcmd.Receipt{MessageID: receipt.MsgID, Stream: receipt.Stream, Sequence: receipt.Sequence})
	if err != nil {
		return Result{}, &DispatchFailedError{Cause: err}
	}
	return resultFromAdmission(selected, !created || receipt.Duplicate), nil
}

func (s *Service) newAdmission(ctx context.Context, req Request, reqID, routeName, dedupeKey, prompt string) (webhookcmd.Admission, error) {
	var reportTo *deliverycmd.Locator
	if req.ReportTo != nil {
		if s.targetResolver == nil {
			return webhookcmd.Admission{}, &DispatchFailedError{Cause: errors.New("report destination resolver is unavailable")}
		}
		resolved, err := s.targetResolver.ResolveTarget(ctx, *req.ReportTo)
		if err != nil {
			return webhookcmd.Admission{}, targetResolutionError(err)
		}
		reportTo = &resolved.Locator
	}
	sessionID := "wh-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	candidate := webhookcmd.Admission{
		RouteName: routeName, DedupeKey: dedupeKey, RequestID: reqID,
		Prompt: prompt, RawBody: &req.RawBody, Source: webhookcmd.SourceExternal,
		SessionID: sessionID, ReportTo: reportTo, CreatedAt: time.Now().UTC(),
	}
	if req.Test {
		candidate.Source = webhookcmd.SourceTest
	}
	_, jobID, err := turncmd.WebhookJobEnvelope(payloadFromAdmission(candidate), routeName, reqID)
	if err != nil {
		return webhookcmd.Admission{}, &InvalidRequestError{Field: "webhook", Message: "cannot prepare job"}
	}
	candidate.JobID = jobID
	return candidate, nil
}

func payloadFromAdmission(admission webhookcmd.Admission) turncmd.SessionTurnPayload {
	return turncmd.SessionTurnPayload{
		Text: admission.Prompt,
		Locator: deliverycmd.Locator{ChannelType: "webhook", AddressKey: admission.SessionID,
			AddressJSON: `{}`, SessionID: admission.SessionID},
		ReportTo:       admission.ReportTo,
		UserID:         admission.SessionID,
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
		ProgressPolicy: deliveryfmt.ProgressPolicy{},
		Deliver:        admission.ReportTo != nil,
		Source:         turncmd.SourceWebhook,
		DedupeKey:      admission.DedupeKey,
	}
}

func resultFromAdmission(admission webhookcmd.Admission, duplicate bool) Result {
	return Result{RequestID: admission.RequestID, MessageID: admission.MessageID,
		Duplicate: duplicate, JobID: admission.JobID, Stream: admission.Stream, Sequence: admission.Sequence}
}

func targetResolutionError(err error) error {
	if errors.Is(err, envelopetarget.ErrSessionUnavailable) ||
		errors.Is(err, envelopetarget.ErrDestinationUnavailable) ||
		errors.Is(err, deliverycmd.ErrDestinationNotFound) {
		return &TargetNotFoundError{Cause: err}
	}
	return &DispatchFailedError{Cause: err}
}
