// Package goalkeeper builds an ADK workflow for goal-directed work with
// validation.
//
// A Goalkeeper workflow runs two caller-provided agents in order:
//
//  1. worker
//  2. validator
//
// The agents run inside one ADK LoopAgent invocation and share the same ADK
// session. The loop stops when the validator's final visible model response
// starts exactly with "verdict: pass", or when the configured maximum iteration
// count is reached. Validator responses that start with "verdict: fail", or
// responses without a recognized verdict, do not stop the loop.
//
// Thought parts are ignored when checking the validator verdict. Only visible
// final response text is considered.
//
// Construct workflows with NewOptions and New:
//
//	workflow, err := goalkeeper.New(goalkeeper.NewOptions(worker, validator))
//	workflow, err := goalkeeper.New(goalkeeper.NewOptions(
//		worker,
//		validator,
//		goalkeeper.WithMaxIterations(3),
//	))
package goalkeeper
