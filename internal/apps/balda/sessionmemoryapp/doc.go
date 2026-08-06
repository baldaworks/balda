// Package sessionmemoryapp owns the Balda session-memory processing lifecycle.
//
// The package depends on transport-neutral session-memory contracts and small
// capture/queue ports. It normalizes exact locator scopes, completed-turn
// identity, and pre-cleanup session boundaries; JetStream is wired at the
// composition root while portable semantic processing remains owned by the
// public sessionmemory/app package.
package sessionmemoryapp
