// Package sessionmemoryapp owns the Balda session-memory processing lifecycle.
//
// The package depends on transport-neutral session-memory contracts and small
// capture/queue ports. It normalizes exact locator scopes and completed-turn
// identity; JetStream and provider implementations are supplied by adapters at
// the composition root.
package sessionmemoryapp
