// Package channel contains transport-neutral balda channel contracts.
package channel

// ProgressPolicy describes which progress indicators a transport supports for
// a specific conversation context.
type ProgressPolicy struct {
	Typing   bool
	Thinking bool
}
