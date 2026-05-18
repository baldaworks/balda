package goalkeeper

import "google.golang.org/adk/agent"

//go:generate go tool options-gen -from-struct=Options -out-filename=options_generated.go

// Options configures the Goalkeeper workflow agent.
type Options struct {
	worker        agent.Agent `option:"mandatory" validate:"required"`
	validator     agent.Agent `option:"mandatory" validate:"required"`
	maxIterations uint        `default:"5" validate:"gt=0"`
}
