package goalkeeper_test

import (
	"fmt"
	"iter"

	"github.com/normahq/balda/internal/apps/balda/goalkeeper"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
)

func ExampleNew() {
	worker, _ := agent.New(agent.Config{
		Name:        "worker",
		Description: "does the requested work",
		Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(func(*session.Event, error) bool) {}
		},
	})
	validator, _ := agent.New(agent.Config{
		Name:        "validator",
		Description: "checks whether the goal is complete",
		Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(func(*session.Event, error) bool) {}
		},
	})

	workflow, _ := goalkeeper.New(goalkeeper.NewOptions(
		worker,
		validator,
		goalkeeper.WithMaxIterations(3),
	))

	fmt.Println(workflow.Name())
	// Output: Goalkeeper
}
