// Package jobs owns durable job state and projection-oriented job services.
//
// It persists job lifecycle state, appends job events, and supports read-model
// updates. It does not own ingress behavior or transport execution.
package jobs
