// Package app owns the portable session-memory application use cases.
//
// The package is deliberately independent from Balda.  Hosts provide narrow
// storage, projection, lifecycle, and structured-model ports; the runtime
// keeps turn, boundary, recall, trace, and forget orchestration behind those
// ports.  Transport, authentication, provider credentials, and delivery
// policy belong to the host adapter.
package app
