package execution

import (
	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
)

const (
	SubjectCommandSession    = actorcmd.SubjectCommandSession
	SubjectCommandJob        = actorcmd.SubjectCommandJob
	SubjectCommandGoal       = actorcmd.SubjectCommandGoal
	SubjectCommandDelivery   = actorcmd.SubjectCommandDelivery
	SubjectCommandMemory     = actorcmd.SubjectCommandMemory
	SubjectCommandControl    = actorcmd.SubjectCommandControl
	SubjectCommandQuestion   = actorcmd.SubjectCommandQuestion
	SubjectCommandPermission = actorcmd.SubjectCommandPermission
	SubjectCommandAll        = actorcmd.SubjectCommandAll

	SubjectEventCommandAccepted     = actorcmd.SubjectEventCommandAccepted
	SubjectEventCommandRunning      = actorcmd.SubjectEventCommandRunning
	SubjectEventCommandInProgress   = actorcmd.SubjectEventCommandInProgress
	SubjectEventCommandAcked        = actorcmd.SubjectEventCommandAcked
	SubjectEventCommandRetrying     = actorcmd.SubjectEventCommandRetrying
	SubjectEventCommandDeadLettered = actorcmd.SubjectEventCommandDeadLettered
	SubjectEventCommandNoop         = actorcmd.SubjectEventCommandNoop
	SubjectEventCommandDecodeFailed = actorcmd.SubjectEventCommandDecodeFailed
	SubjectEventJobCreated          = actorcmd.SubjectEventJobCreated
	SubjectEventJobUpdated          = actorcmd.SubjectEventJobUpdated
	SubjectEventJobCompleted        = actorcmd.SubjectEventJobCompleted
	SubjectEventDeliverySent        = actorcmd.SubjectEventDeliverySent
	SubjectEventDeliveryFailed      = actorcmd.SubjectEventDeliveryFailed
	SubjectEventMemoryUpdated       = actorcmd.SubjectEventMemoryUpdated
	SubjectEventAll                 = actorcmd.SubjectEventAll

	SubjectDLQCommand = actorcmd.SubjectDLQCommand
	SubjectDLQAll     = actorcmd.SubjectDLQAll

	HeaderEnvelopeID    = actorcmd.HeaderEnvelopeID
	HeaderCorrelationID = actorcmd.HeaderCorrelationID
	HeaderCausationID   = actorcmd.HeaderCausationID
	HeaderDedupeKey     = actorcmd.HeaderDedupeKey
	HeaderNamespace     = actorcmd.HeaderNamespace
	HeaderActorKey      = actorcmd.HeaderActorKey
	HeaderPriority      = actorcmd.HeaderPriority
)

func SubjectForEnvelope(env actorlayer.Envelope) string { return actorcmd.SubjectForEnvelope(env) }

func EnvelopeHeaders(env actorlayer.Envelope) map[string]string { return actorcmd.EnvelopeHeaders(env) }
