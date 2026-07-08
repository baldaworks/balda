// Package engine provides transport-agnostic actor delivery execution.
//
// Runtime serializes deliveries by resolver lane, calls the supplied actor
// handler, and settles each delivery through Ack, Retry, or DeadLetter.
// InProgress is a delivery hook for host adapters that own heartbeat cadence;
// Runtime exposes EmitInProgress for event publication but does not start a
// heartbeat loop on its own.
package engine
