// Package dispatch provides actor registration and address resolution helpers.
//
// MemoryRegistry normalizes addresses by trimming whitespace and lowercasing
// before registration or lookup. When an exact address is not registered,
// lookups fall back to a target wildcard address such as "session:*".
package dispatch
