package state

import (
	"context"
	"fmt"
	"strings"
)

func findFinalDelivery(ctx context.Context, jobID string,
	get func(context.Context, string) (DeliveryRecord, bool, error)) (DeliveryRecord, bool, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return DeliveryRecord{}, false, fmt.Errorf("job id is required")
	}
	var pending DeliveryRecord
	for _, suffix := range []string{"final", "terminal"} {
		record, found, err := get(ctx, jobID+":delivery:"+suffix)
		if err != nil {
			return DeliveryRecord{}, false, err
		}
		if !found || record.JobID != jobID {
			continue
		}
		if record.Status == DeliveryStatusSent {
			return record, true, nil
		}
		if pending.DeliveryKey == "" {
			pending = record
		}
	}
	return pending, pending.DeliveryKey != "", nil
}
