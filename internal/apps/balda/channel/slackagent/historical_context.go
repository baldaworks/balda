package slackagent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/rs/zerolog"
)

const historicalMarkerLimit = 999

// ThreadContextResult contains the bounded prompt and persisted historical attachments.
type ThreadContextResult struct {
	Prompt      string
	Attachments []attachment.Descriptor
}

// HistoricalContextHydrator enriches a Slack thread snapshot with bounded historical media.
type HistoricalContextHydrator interface {
	Hydrate(ctx context.Context, snapshot ThreadSnapshot, currentRequest string, current []attachment.Descriptor) (ThreadContextResult, error)
}

type historicalContextHydrator struct {
	client FileClient
	blobs  BlobStore
	limits attachment.Limits
	logger zerolog.Logger
}

type historicalFileOccurrence struct {
	messageIndex int
	order        int
	groupIndex   int
	file         FileRef
}

type historicalFileGroup struct {
	reference      string
	representative historicalFileOccurrence
	status         historicalFileStatus
	reason         string
	file           FileRef
	descriptor     *attachment.Descriptor
}

// NewHistoricalContextHydrator constructs bounded Slack thread-media hydration.
func NewHistoricalContextHydrator(client FileClient, blobs BlobStore, limits attachment.Limits, logger zerolog.Logger) HistoricalContextHydrator {
	return &historicalContextHydrator{
		client: client,
		blobs:  blobs,
		limits: limits,
		logger: logger.With().Str("component", "balda.channel.slackagent.history_files").Logger(),
	}
}

func (h *historicalContextHydrator) Hydrate(
	ctx context.Context,
	snapshot ThreadSnapshot,
	currentRequest string,
	current []attachment.Descriptor,
) (ThreadContextResult, error) {
	messages, truncated := selectContextMessages(snapshot)
	snapshot.Messages = messages
	snapshot.Truncated = truncated

	groups, occurrences, markerTruncated := collectHistoricalFileGroups(messages)
	if markerTruncated {
		snapshot.Truncated = true
	}
	if len(groups) == 0 {
		return formatHistoricalContext(snapshot, currentRequest, nil)
	}
	if h == nil || h.client == nil || h.blobs == nil {
		return ThreadContextResult{}, newFileError("", "history_media_unavailable", true, nil)
	}
	if !validAttachmentLimits(h.limits) {
		return ThreadContextResult{}, newFileError("", "invalid_attachment_limits", true, nil)
	}
	currentCount, currentBytes, err := currentAttachmentUsage(current, h.limits)
	if err != nil {
		return ThreadContextResult{}, err
	}

	remainingCount := h.limits.MaxFilesPerMessage - currentCount
	remainingBytes := h.limits.MaxTotalBytes - currentBytes
	if !h.blobs.Enabled() {
		for i := range groups {
			groups[i].status = historicalFileUnavailable
			groups[i].reason = "attachment_store_disabled"
		}
	} else {
		order := make([]int, len(groups))
		for i := range groups {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool {
			return groups[order[i]].representative.order > groups[order[j]].representative.order
		})

		for _, groupIndex := range order {
			group := &groups[groupIndex]
			persisted, outcome, hydrateErr := h.hydrateGroup(ctx, group.file, remainingCount, remainingBytes)
			if hydrateErr != nil {
				h.logOutcome("history_hydration", group.file, currentCount, currentBytes, "retry", fileFailureReason(hydrateErr))
				return ThreadContextResult{}, hydrateErr
			}
			group.status = outcome.status
			group.reason = outcome.reason
			group.file = outcome.file
			if persisted == nil {
				h.logOutcome("history_hydration", group.file, currentCount, currentBytes, "terminal", group.reason)
				continue
			}
			group.descriptor = persisted
			remainingCount--
			remainingBytes -= persisted.SizeBytes
		}
	}

	applyHistoricalFileMarkers(snapshot.Messages, groups, occurrences)
	formatted, err := formatThreadContext(snapshot, currentRequest)
	if err != nil {
		return ThreadContextResult{}, newFileError("", "history_context_format_failed", true, err)
	}
	retained := make(map[string]struct{}, len(formatted.RetainedReferences))
	for _, reference := range formatted.RetainedReferences {
		retained[reference] = struct{}{}
	}
	attachments := retainedHistoricalDescriptors(groups, occurrences, retained)
	h.logSummary(groups, attachments, currentCount, currentBytes)
	return ThreadContextResult{Prompt: formatted.Prompt, Attachments: attachments}, nil
}

type historicalFileOutcome struct {
	status historicalFileStatus
	reason string
	file   FileRef
}

func (h *historicalContextHydrator) hydrateGroup(
	ctx context.Context,
	file FileRef,
	remainingCount int,
	remainingBytes int64,
) (*attachment.Descriptor, historicalFileOutcome, error) {
	file = normalizeFileRef(file)
	outcome := historicalFileOutcome{status: historicalFileUnavailable, reason: mediaUnavailableReason, file: file}
	if file.ID == "" {
		outcome.reason = "missing_file_id"
		return nil, outcome, nil
	}
	if remainingCount <= 0 {
		outcome.status = historicalFileOverBudget
		outcome.reason = "too_many_files"
		return nil, outcome, nil
	}
	if declaredHistoricalFileOverBudget(file, h.limits.MaxFileBytes, remainingBytes) {
		outcome.status = historicalFileOverBudget
		outcome.reason = historicalBudgetReason(file, h.limits.MaxFileBytes, remainingBytes)
		return nil, outcome, nil
	}
	if unsupportedHistoricalFile(file) {
		outcome.status = historicalFileUnsupported
		outcome.reason = "unsupported_file_mode"
		return nil, outcome, nil
	}

	resolved, err := h.client.ResolveFile(ctx, file)
	if err != nil {
		if retryableHistoricalFileError(err) {
			return nil, outcome, err
		}
		outcome.reason = fileFailureReason(err)
		return nil, outcome, nil
	}
	resolved = normalizeFileRef(resolved)
	outcome.file = resolved
	if unsupportedHistoricalFile(resolved) {
		outcome.status = historicalFileUnsupported
		outcome.reason = "unsupported_file_mode"
		return nil, outcome, nil
	}
	if declaredHistoricalFileOverBudget(resolved, h.limits.MaxFileBytes, remainingBytes) {
		outcome.status = historicalFileOverBudget
		outcome.reason = historicalBudgetReason(resolved, h.limits.MaxFileBytes, remainingBytes)
		return nil, outcome, nil
	}

	body, err := h.client.DownloadFile(ctx, resolved)
	if err != nil {
		if retryableHistoricalFileError(err) {
			return nil, outcome, err
		}
		outcome.reason = fileFailureReason(err)
		return nil, outcome, nil
	}
	if body == nil {
		return nil, outcome, newFileError(resolved.ID, "download_unavailable", true, nil)
	}
	maxBytes := min(h.limits.MaxFileBytes, remainingBytes)
	persisted, persistErr := h.blobs.Persist(ctx, resolved.descriptor(), body, maxBytes)
	_ = body.Close()
	if persistErr != nil {
		switch {
		case errors.Is(persistErr, attachment.ErrTooLarge):
			outcome.status = historicalFileOverBudget
			outcome.reason = fileSizeExceededReason
			if remainingBytes < h.limits.MaxFileBytes {
				outcome.reason = totalSizeExceededReason
			}
			return nil, outcome, nil
		case errors.Is(persistErr, attachment.ErrStoreDisabled):
			outcome.reason = "attachment_store_disabled"
			return nil, outcome, nil
		default:
			return nil, outcome, newFileError(resolved.ID, "storage_unavailable", true, persistErr)
		}
	}
	if persisted.Blob == nil || strings.TrimSpace(persisted.Blob.Path) == "" || persisted.SizeBytes < 0 {
		return nil, outcome, newFileError(resolved.ID, "invalid_persisted_blob", true, nil)
	}
	if persisted.SizeBytes > remainingBytes || persisted.SizeBytes > h.limits.MaxFileBytes {
		outcome.status = historicalFileOverBudget
		outcome.reason = historicalBudgetReason(FileRef{SizeBytes: persisted.SizeBytes}, h.limits.MaxFileBytes, remainingBytes)
		return nil, outcome, nil
	}
	normalized, ok := attachment.Normalize(persisted)
	if !ok || normalized.Blob == nil || strings.TrimSpace(normalized.Blob.Path) == "" {
		return nil, outcome, newFileError(resolved.ID, "invalid_persisted_blob", true, nil)
	}
	outcome.status = historicalFileSupplied
	outcome.reason = ""
	return &normalized, outcome, nil
}

func collectHistoricalFileGroups(messages []ThreadMessage) ([]historicalFileGroup, []historicalFileOccurrence, bool) {
	groupByFileID := make(map[string]int)
	var groups []historicalFileGroup
	var occurrences []historicalFileOccurrence
	truncated := false

collect:
	for messageIndex, message := range messages {
		for _, file := range message.files {
			if len(occurrences) == historicalMarkerLimit {
				truncated = true
				break collect
			}
			file = normalizeFileRef(file)
			groupIndex, ok := groupByFileID[file.ID]
			if file.ID == "" || !ok {
				groupIndex = len(groups)
				groups = append(groups, historicalFileGroup{
					reference: fmt.Sprintf("history_attachment_%03d", groupIndex+1),
					file:      file,
				})
				if file.ID != "" {
					groupByFileID[file.ID] = groupIndex
				}
			}
			occurrence := historicalFileOccurrence{
				messageIndex: messageIndex,
				order:        len(occurrences),
				groupIndex:   groupIndex,
				file:         file,
			}
			occurrences = append(occurrences, occurrence)
			groups[groupIndex].representative = occurrence
			groups[groupIndex].file = file
		}
	}
	return groups, occurrences, truncated
}

func applyHistoricalFileMarkers(messages []ThreadMessage, groups []historicalFileGroup, occurrences []historicalFileOccurrence) {
	for i := range messages {
		messages[i].fileMarkers = nil
	}
	for _, occurrence := range occurrences {
		group := groups[occurrence.groupIndex]
		file := occurrence.file
		status := historicalFileDuplicate
		reason := "duplicate_file"
		if occurrence.order == group.representative.order {
			file = group.file
			status = group.status
			reason = group.reason
		}
		messages[occurrence.messageIndex].fileMarkers = append(messages[occurrence.messageIndex].fileMarkers, historicalFileMarker{
			Reference: group.reference,
			FileID:    file.ID,
			Name:      firstNonEmpty(file.Name, file.Title),
			MIMEClass: fileMIMEClass(file.MIMEType),
			SizeBytes: file.SizeBytes,
			Status:    status,
			Reason:    reason,
		})
	}
}

func retainedHistoricalDescriptors(
	groups []historicalFileGroup,
	occurrences []historicalFileOccurrence,
	retained map[string]struct{},
) []attachment.Descriptor {
	attachments := make([]attachment.Descriptor, 0, len(retained))
	for _, occurrence := range occurrences {
		group := groups[occurrence.groupIndex]
		if occurrence.order != group.representative.order || group.descriptor == nil {
			continue
		}
		if _, ok := retained[group.reference]; !ok {
			continue
		}
		attachments = append(attachments, *group.descriptor)
	}
	return attachments
}

func currentAttachmentUsage(current []attachment.Descriptor, limits attachment.Limits) (int, int64, error) {
	if len(current) > limits.MaxFilesPerMessage {
		return 0, 0, newFileError("", "invalid_current_attachment_budget", true, nil)
	}
	var total int64
	for _, descriptor := range current {
		if descriptor.Blob == nil || strings.TrimSpace(descriptor.Blob.Path) == "" || descriptor.SizeBytes < 0 || descriptor.SizeBytes > limits.MaxFileBytes || descriptor.SizeBytes > math.MaxInt64-total {
			return 0, 0, newFileError(descriptor.FileID, "invalid_current_attachment_budget", true, nil)
		}
		total += descriptor.SizeBytes
	}
	if total > limits.MaxTotalBytes {
		return 0, 0, newFileError("", "invalid_current_attachment_budget", true, nil)
	}
	return len(current), total, nil
}

func declaredHistoricalFileOverBudget(file FileRef, maxFileBytes, remainingBytes int64) bool {
	return file.SizeBytes > maxFileBytes || file.SizeBytes > remainingBytes
}

func historicalBudgetReason(file FileRef, maxFileBytes, remainingBytes int64) string {
	if file.SizeBytes > maxFileBytes {
		return fileSizeExceededReason
	}
	return totalSizeExceededReason
}

func unsupportedHistoricalFile(file FileRef) bool {
	if strings.TrimSpace(file.FileAccess) == checkFileInfoAccess {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(file.Mode)) {
	case "", "hosted":
		return false
	default:
		return true
	}
}

func retryableHistoricalFileError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsRetryableSlackError(err)
}

func formatHistoricalContext(snapshot ThreadSnapshot, currentRequest string, attachments []attachment.Descriptor) (ThreadContextResult, error) {
	formatted, err := formatThreadContext(snapshot, currentRequest)
	if err != nil {
		return ThreadContextResult{}, err
	}
	return ThreadContextResult{Prompt: formatted.Prompt, Attachments: attachments}, nil
}

func (h *historicalContextHydrator) logOutcome(stage string, file FileRef, currentCount int, currentBytes int64, settlement, reason string) {
	h.logger.Warn().
		Str("stage", stage).
		Str("reason", sanitizeFailureCode(reason)).
		Str("file_id", safeFileID(file.ID)).
		Str("mime_class", fileMIMEClass(file.MIMEType)).
		Str("settlement_class", settlement).
		Int("current_file_count", currentCount).
		Int64("current_bytes", currentBytes).
		Int64("declared_bytes", max(file.SizeBytes, 0)).
		Msg("handled historical Slack file")
}

func (h *historicalContextHydrator) logSummary(groups []historicalFileGroup, attachments []attachment.Descriptor, currentCount int, currentBytes int64) {
	var declaredBytes, persistedBytes int64
	var supplied int
	files := make([]FileRef, 0, len(groups))
	for _, group := range groups {
		files = append(files, group.file)
		if group.file.SizeBytes > 0 && group.file.SizeBytes <= math.MaxInt64-declaredBytes {
			declaredBytes += group.file.SizeBytes
		}
		if group.descriptor != nil {
			supplied++
		}
	}
	for _, descriptor := range attachments {
		if descriptor.SizeBytes > 0 && descriptor.SizeBytes <= math.MaxInt64-persistedBytes {
			persistedBytes += descriptor.SizeBytes
		}
	}
	h.logger.Info().
		Str("stage", "history_complete").
		Str("reason", "accepted").
		Str("mime_class", fileSetMIMEClass(files, "")).
		Str("settlement_class", "accepted").
		Int("current_file_count", currentCount).
		Int64("current_bytes", currentBytes).
		Int("candidate_count", len(groups)).
		Int("selected_count", supplied).
		Int("retained_count", len(attachments)).
		Int("omitted_count", len(groups)-len(attachments)).
		Int64("declared_bytes", declaredBytes).
		Int64("persisted_bytes", persistedBytes).
		Msg("hydrated historical Slack files")
}
