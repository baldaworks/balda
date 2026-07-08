// Package transport defines actorlayer transport interfaces.
//
// Implementations dispatch durable envelopes and publish or consume envelope
// events. Transport implementations should validate envelopes before sending
// them across a broker or other process boundary.
package transport
