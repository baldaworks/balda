package state

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type ScheduleMutationKind string

const (
	ScheduleCreate    ScheduleMutationKind = "create"
	ScheduleEdit      ScheduleMutationKind = "edit"
	ScheduleSelection ScheduleMutationKind = "selection"
	ScheduleDelete    ScheduleMutationKind = "delete"
)

// ScheduleMutation commits one managed definition change with its audit record.
type ScheduleMutation struct {
	Kind            ScheduleMutationKind
	Record          ScheduledJobRecord
	ExpectedVersion uint64
	Authority       schedulecmd.Authority
	Audit           usercmd.AuditEvent
}

// ScheduleManagementStore owns authority-checked, versioned schedule writes.
type ScheduleManagementStore interface {
	CheckAuthority(ctx context.Context, authority schedulecmd.Authority) error
	Save(ctx context.Context, mutation ScheduleMutation) error
	AdmitManualRun(ctx context.Context, admission ScheduleManualAdmission) (bool, error)
}

// ScheduleManualAdmission atomically checks current authority and definition selection.
type ScheduleManualAdmission struct {
	Run             ScheduleRunRecord
	ExpectedVersion uint64
	ConfirmDisabled bool
	Authority       schedulecmd.Authority
	Audit           usercmd.AuditEvent
}
